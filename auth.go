package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"golang.org/x/term"
)

const (
	callbackPort = 8765
	callbackPath = "/callback"
)

// runAuthFlow runs the one-time OAuth flow. cliID/cliSecret come from the
// `auth` command's --client-id/--client-secret flags (which Viper also binds to
// LINKEDIN_CLIENT_ID/SECRET and the config file); empty values fall through to
// resolveAuthCredentials' env/file/prompt precedence.
func runAuthFlow(cliID, cliSecret string) error {
	creds, persist, err := resolveAuthCredentials(cliID, cliSecret)
	if err != nil {
		return err
	}
	if persist {
		if err := saveAppCredentials(creds); err != nil {
			return fmt.Errorf("credentials resolved but save failed: %w", err)
		}
		fmt.Fprintf(os.Stderr, "credentials saved to %s\n", appCredsPath())
	}

	if !portAvailable(callbackPort) {
		return fmt.Errorf("port %d is in use — close whatever is listening on it and retry", callbackPort)
	}

	state, err := randomState()
	if err != nil {
		return err
	}
	redirectURI := fmt.Sprintf("http://localhost:%d%s", callbackPort, callbackPath)
	authURL := buildAuthorizeURL(creds.ClientID, redirectURI, state)

	fmt.Fprintln(os.Stderr, "opening LinkedIn authorization in your browser...")
	fmt.Fprintln(os.Stderr, "if it does not open, visit this URL manually:")
	fmt.Fprintln(os.Stderr, "  "+authURL)

	if err := openURL(authURL); err != nil {
		fmt.Fprintf(os.Stderr, "(could not auto-open browser: %v)\n", err)
	}

	code, err := waitForCallback(state)
	if err != nil {
		return err
	}

	tr, err := exchangeCodeForTokens(creds, code, redirectURI)
	if err != nil {
		return err
	}

	info, err := fetchUserSubject(tr.AccessToken)
	if err != nil {
		return fmt.Errorf("fetched tokens but userinfo failed: %w", err)
	}

	store := tokenStore{
		AccessToken:  tr.AccessToken,
		RefreshToken: tr.RefreshToken,
		ExpiresAt:    time.Now().Add(time.Duration(tr.ExpiresIn) * time.Second),
		PersonURN:    "urn:li:person:" + info.Sub,
	}
	if err := saveTokens(store); err != nil {
		return fmt.Errorf("tokens fetched but save failed: %w", err)
	}

	fmt.Printf("authenticated as %s", info.Name)
	if info.Email != "" {
		fmt.Printf(" (%s)", info.Email)
	}
	fmt.Println()
	fmt.Printf("person URN: %s\n", store.PersonURN)
	fmt.Printf("tokens saved to %s\n", tokensPath())
	fmt.Printf("access token valid until %s\n", store.ExpiresAt.Format("2006-01-02 15:04 MST"))
	return nil
}

func randomState() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func buildAuthorizeURL(clientID, redirectURI, state string) string {
	v := url.Values{}
	v.Set("response_type", "code")
	v.Set("client_id", clientID)
	v.Set("redirect_uri", redirectURI)
	v.Set("state", state)
	v.Set("scope", oauthScopes)
	return linkedinAuthorizeURL + "?" + v.Encode()
}

// resolveAuthCredentials picks the LinkedIn app credentials following the
// documented precedence (flags > env > file > interactive prompt) and reports
// whether the caller should persist them to app.json. Flags and prompt return
// persist=true; env vars and pre-existing app.json return persist=false.
func resolveAuthCredentials(cliID, cliSecret string) (appCreds, bool, error) {
	if cliID != "" && cliSecret != "" {
		return appCreds{ClientID: cliID, ClientSecret: cliSecret}, true, nil
	}
	if (cliID == "") != (cliSecret == "") {
		return appCreds{}, false, errors.New("--client-id and --client-secret must be provided together")
	}

	if id, secret := os.Getenv("LINKEDIN_CLIENT_ID"), os.Getenv("LINKEDIN_CLIENT_SECRET"); id != "" && secret != "" {
		return appCreds{ClientID: id, ClientSecret: secret}, false, nil
	}

	if c, ok, err := loadAppCredentialsFromFile(); err != nil {
		return appCreds{}, false, err
	} else if ok {
		return c, false, nil
	}

	c, err := promptForCredentials()
	if err != nil {
		return appCreds{}, false, err
	}
	return c, true, nil
}

func promptForCredentials() (appCreds, error) {
	var c appCreds
	fmt.Fprintln(os.Stderr, "no LinkedIn app credentials found in flags, env, or config file.")
	fmt.Fprintln(os.Stderr, "find them at https://www.linkedin.com/developers/apps → your app → Auth tab.")
	fmt.Fprintln(os.Stderr)

	id, err := promptLine("Client ID: ")
	if err != nil {
		return c, err
	}
	c.ClientID = strings.TrimSpace(id)
	if c.ClientID == "" {
		return c, errors.New("Client ID cannot be empty")
	}

	secret, err := promptSecret("Client Secret (hidden): ")
	if err != nil {
		return c, err
	}
	c.ClientSecret = strings.TrimSpace(secret)
	if c.ClientSecret == "" {
		return c, errors.New("Client Secret cannot be empty")
	}
	return c, nil
}

func promptLine(prompt string) (string, error) {
	fmt.Fprint(os.Stderr, prompt)
	reader := bufio.NewReader(os.Stdin)
	line, err := reader.ReadString('\n')
	if err != nil {
		return "", err
	}
	return line, nil
}

func promptSecret(prompt string) (string, error) {
	fmt.Fprint(os.Stderr, prompt)
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		fmt.Fprintln(os.Stderr, "(stdin is not a terminal — input will be echoed)")
		return promptLine("")
	}
	b, err := term.ReadPassword(fd)
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func portAvailable(port int) bool {
	l, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		return false
	}
	_ = l.Close()
	return true
}

type callbackResult struct {
	code string
	err  error
}

func waitForCallback(expectedState string) (string, error) {
	ch := make(chan callbackResult, 1)
	srv := &http.Server{Addr: fmt.Sprintf(":%d", callbackPort)}
	mux := http.NewServeMux()
	mux.HandleFunc(callbackPath, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()

		if errMsg := q.Get("error"); errMsg != "" {
			detail := q.Get("error_description")
			fmt.Fprintf(w, "LinkedIn returned an error: %s\n%s\n\nYou can close this tab.", errMsg, detail)
			ch <- callbackResult{err: fmt.Errorf("OAuth error from LinkedIn: %s — %s", errMsg, detail)}
			return
		}
		gotState := q.Get("state")
		if gotState != expectedState {
			fmt.Fprintln(w, "state mismatch — possible CSRF. You can close this tab.")
			ch <- callbackResult{err: errors.New("state mismatch in OAuth callback")}
			return
		}
		code := q.Get("code")
		if code == "" {
			fmt.Fprintln(w, "no code in callback")
			ch <- callbackResult{err: errors.New("no code in callback")}
			return
		}
		fmt.Fprintln(w, "Authorization received. You can close this tab and return to the terminal.")
		ch <- callbackResult{code: code}
	})
	srv.Handler = mux

	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			ch <- callbackResult{err: err}
		}
	}()

	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	}()

	select {
	case res := <-ch:
		return res.code, res.err
	case <-time.After(5 * time.Minute):
		return "", errors.New("OAuth callback timed out after 5 minutes")
	}
}
