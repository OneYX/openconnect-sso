package internal

import (
	"context"
	"fmt"
	"github.com/chromedp/chromedp"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
)

type OpenconnectCtx struct {
	process         *exec.Cmd
	client          *http.Client
	exit            chan os.Signal
	cookieFoundChan chan string
	exitChan        chan os.Signal
	targetUrl       string
	server          string
	username        string
	password        string
	authGroup       string
	browserCtx      context.Context
	closeBrowser    context.CancelFunc
}

func NewOpenconnectCtx(server, username, password, authgroup string) *OpenconnectCtx {
	client := NewHttpClient(server)
	exit := make(chan os.Signal)

	// register exit signals
	signal.Notify(exit, os.Kill, os.Interrupt)

	return &OpenconnectCtx{
		client:          client,
		cookieFoundChan: make(chan string),
		exitChan:        exit,
		targetUrl:       getActualUrl(client, server),
		username:        username,
		password:        password,
		authGroup:       authgroup,
	}
}

func (oc *OpenconnectCtx) Run() error {
	samlAuth, err := oc.AuthenticationInit()
	if err != nil {
		log.Println("Could not start authentication process...")
		return err
	}

	tasks, err := oc.startBrowser(samlAuth)
	if err != nil {
		log.Println("Could not start browser properly...")
		return err
	}

	// close browser at the end - no matter what happens
	defer oc.closeBrowser()

	// handle exit signal
	log.Println("Starting goroutine to handle exit signals")
	go oc.handleExit()

	log.Println("Starting goroutine to search for cookie", samlAuth.Auth.SsoV2TokenCookieName)
	go oc.browserCookieFinder(samlAuth.Auth.SsoV2TokenCookieName)

	log.Println("Open browser and navigate to SSO login page : ", samlAuth.Auth.SsoV2Login)
	err = chromedp.Run(oc.browserCtx, tasks)
	if err != nil {
		return err
	}

	// consume cookie and connect to vpn
	return oc.startVpnOnLoginCookie(samlAuth)
}

func (oc *OpenconnectCtx) startBrowser(samlAuth *AuthenticationInitExpectedResponse) (chromedp.Tasks, error) {
	oc.browserCtx, oc.closeBrowser = createBrowserContext()
	tasks := oc.generateDefaultBrowserTasks(samlAuth)

	// setup listener to exit program when browser is closed
	chromedp.ListenTarget(oc.browserCtx, func(ev interface{}) {
		closeBrowserOnRenderProcessGone(ev, oc.exitChan)
	})

	return tasks, nil
}

// startVpnOnLoginCookie waits to get a cookie from the authenticationCookies channel before confirming
// the authentication process (to get token/cert) and then starting openconnect
func (oc *OpenconnectCtx) startVpnOnLoginCookie(auth *AuthenticationInitExpectedResponse) error {
	for cookie := range oc.cookieFoundChan {
		token, cert, err := oc.AuthenticationConfirmation(auth, cookie)
		oc.closeBrowser() // close browser

		if err != nil {
			return err
		}

		oc.process = exec.Command("sudo",
			"openconnect",
			fmt.Sprintf("--useragent=AnyConnect Linux_64 %s", VERSION),
			fmt.Sprintf("--version-string=%s", VERSION),
			fmt.Sprintf("--cookie=%s", token),
			fmt.Sprintf("--servercert=%s", cert),
			oc.targetUrl,
		)

		oc.process.Stdout = os.Stdout
		oc.process.Stderr = os.Stdout
		oc.process.Stdin = os.Stdin

		log.Println("Starting openconnect: ", oc.process.String())
		// 保存认证信息

		return oc.process.Run()
	}

	return nil
}
