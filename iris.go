//Package iris the fastest go web framework in (this) Earth.
///NOTE: When you see 'framework' or 'station' we mean the Iris web framework's main implementation.
//
//
// Basic usage
// ----------------------------------------------------------------------
//
// package main
//
// import  "github.com/kataras/iris"
//
// func main() {
//     iris.Get("/hi_json", func(c *iris.Context) {
//         c.JSON(200, iris.Map{
//             "Name": "Iris",
//             "Age":  2,
//         })
//     })
//     iris.Listen(":8080")
// }
//
// ----------------------------------------------------------------------
//
// package main
//
// import  "github.com/kataras/iris"
//
// func main() {
// 	s1 := iris.New()
// 	s1.Get("/hi_json", func(c *iris.Context) {
// 		c.JSON(200, iris.Map{
// 			"Name": "Iris",
// 			"Age":  2,
// 		})
// 	})
//
// 	s2 := iris.New()
// 	s2.Get("/hi_raw_html", func(c *iris.Context) {
// 		c.HTML(iris.StatusOK, "<b> Iris </b> welcomes <h1>you!</h1>")
// 	})
//
// 	go s1.Listen(":8080")
// 	s2.Listen(":1993")
// }
//
// -----------------------------DOCUMENTATION----------------------------
// ----------------------------_______________---------------------------
// For middleware, templates, sessions, websockets, mails, subdomains,
// dynamic subdomains, routes, party of subdomains & routes and much more
// visit https://www.gitbook.com/book/kataras/iris/details
package iris // import "github.com/kataras/iris"

import (
	"fmt"
	"net/http"
	"os"
	"path"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"sync"

	"github.com/gavv/httpexpect"
	"github.com/iris-contrib/errors"
	"github.com/iris-contrib/logger"
	"github.com/iris-contrib/rest"
	"github.com/iris-contrib/template/html"
	"github.com/kataras/iris/config"
	"github.com/kataras/iris/context"
	"github.com/kataras/iris/utils"
	"github.com/kataras/iris/websocket"
	"github.com/valyala/fasthttp"
)

const (
	// Version of the iris
	Version = "4.0.0-alpha.2"

	banner = `         _____      _
        |_   _|    (_)
          | |  ____ _  ___
          | | | __|| |/ __|
         _| |_| |  | |\__ \
        |_____|_|  |_||___/ ` + Version + ` `
)

// Default entry, use it with iris.$anyPublicFunc
var (
	Default   *Framework
	Config    *config.Iris
	Logger    *logger.Logger
	Plugins   PluginContainer
	Websocket websocket.Server
	Servers   *ServerList
	// Available is a channel type of bool, fired to true when the server is opened and all plugins ran
	// never fires false, if the .Close called then the channel is re-allocating.
	// the channel is closed only when .ListenVirtual is used, otherwise it remains open until you close it.
	//
	// Note: it is a simple channel and decided to put it here and no inside HTTPServer, doesn't have statuses just true and false, simple as possible
	// Where to use that?
	// this is used on extreme cases when you don't know which .Listen/.ListenVirtual will be called
	// and you want to run/declare something external-not-Iris (all Iris functionality declared before .Listen/.ListenVirtual) AFTER the server is started and plugins finished.
	// see the server_test.go for an example
	Available chan bool
)

func initDefault() {
	Default = New()
	Config = Default.Config
	Logger = Default.Logger
	Plugins = Default.Plugins
	Websocket = Default.Websocket
	Servers = Default.Servers
	Available = Default.Available
}

func init() {
	initDefault()
}

// -------------------------------------------------------------------------------------
// -------------------------------------------------------------------------------------
// --------------------------------Framework implementation-----------------------------
// -------------------------------------------------------------------------------------
// -------------------------------------------------------------------------------------

type (
	// FrameworkAPI contains the main Iris Public API
	FrameworkAPI interface {
		MuxAPI
		Must(error)
		AddServer(config.Server) *Server
		ListenTo(config.Server) error
		Listen(string)
		ListenTLS(string, string, string)
		ListenUNIX(string, os.FileMode)
		ListenVirtual(...string) *Server
		Go() error
		Close() error
		UseSessionDB(SessionDatabase)
		UseTemplate(TemplateEngine) *TemplateEngineLocation
		UseGlobal(...Handler)
		UseGlobalFunc(...HandlerFunc)
		OnError(int, HandlerFunc)
		EmitError(int, *Context)
		Lookup(string) Route
		Lookups() []Route
		Path(string, ...interface{}) string
		URL(string, ...interface{}) string
		TemplateString(string, interface{}, ...map[string]interface{}) string
		Tester(t *testing.T) *httpexpect.Expect
	}

	// Framework is our God |\| Google.Search('Greek mythology Iris')
	//
	// Implements the FrameworkAPI
	Framework struct {
		*muxAPI
		rest      *rest.Render
		sessions  *sessionsManager
		templates *TemplateEngines

		// fields which are useful to the user/dev
		// the last  added server is the main server
		Servers *ServerList
		Config  *config.Iris
		// configuration by instance.Logger.Config
		Logger    *logger.Logger
		Plugins   PluginContainer
		Websocket websocket.Server
		Available chan bool
		// this is setted once when .Tester(t) is called
		testFramework *httpexpect.Expect
	}
)

var _ FrameworkAPI = &Framework{}

// New creates and returns a new Iris station aka Framework.
//
// Receives an optional config.Iris as parameter
// If empty then config.Default() is used instead
func New(cfg ...config.Iris) *Framework {
	c := config.Default().Merge(cfg)

	// we always use 's' no 'f' because 's' is easier for me to remember because of 'station'
	// some things never change :)
	s := &Framework{Config: &c, Available: make(chan bool)}
	{
		///NOTE: set all with s.Config pointer
		// set the Logger
		s.Logger = logger.New(logger.DefaultConfig())
		// set the plugin container
		s.Plugins = &pluginContainer{logger: s.Logger}
		// set the templates
		s.templates = &TemplateEngines{
			Helpers: map[string]interface{}{
				"url":     s.URL,
				"urlpath": s.Path,
			},
			Engines: make([]*TemplateEngineWrapper, 0),
		}
		//set the session manager
		s.sessions = newSessionsManager(c.Sessions)
		// set the websocket server
		s.Websocket = websocket.NewServer(s.Config.Websocket)
		// set the servemux, which will provide us the public API also, with its context pool
		mux := newServeMux(sync.Pool{New: func() interface{} { return &Context{framework: s} }}, s.Logger)
		mux.onLookup = s.Plugins.DoPreLookup
		// set the public router API (and party)
		s.muxAPI = &muxAPI{mux: mux, relativePath: "/"}

		s.Servers = &ServerList{mux: mux, servers: make([]*Server, 0)}
	}

	return s
}

func (s *Framework) initialize() {
	// set the rest
	s.rest = rest.New(s.Config.Rest)
	// prepare the templates if enabled
	if !s.Config.DisableTemplateEngines {
		if err := s.templates.LoadAll(); err != nil {
			s.Logger.Panic(err) // panic on templates loading before listening if we have an error.
		}
		// check and prepare the templates
		if len(s.templates.Engines) == 0 { // no template engine is registered, let's use the default
			s.UseTemplate(html.New())
		}
		s.templates.Reload = s.Config.IsDevelopment
	}

	// listen to websocket connections
	websocket.RegisterServer(s, s.Websocket, s.Logger)

	//  prepare the mux & the server
	s.mux.setCorrectPath(!s.Config.DisablePathCorrection)
	s.mux.setEscapePath(!s.Config.DisablePathEscape)
	// set the debug profiling handlers if ProfilePath is setted
	if debugPath := s.Config.ProfilePath; debugPath != "" {
		s.Handle(MethodGet, debugPath+"/*action", profileMiddleware(debugPath)...)
	}
}

// Go starts the iris station, listens to all registered servers, and prepare only if Virtual
func Go() error {
	return Default.Go()
}

// Go starts the iris station, listens to all registered servers, and prepare only if Virtual
func (s *Framework) Go() error {
	s.initialize()
	s.Plugins.DoPreListen(s)

	if firstErr := s.Servers.OpenAll(); firstErr != nil {
		return firstErr
	}

	// print the banner
	if !s.Config.DisableBanner {
		openedServers := s.Servers.GetAllOpened()
		l := len(openedServers)
		hosts := make([]string, l, l)
		for i, srv := range openedServers {
			hosts[i] = srv.Host()
		}

		bannerMessage := time.Now().Format(config.TimeFormat) + ": Running at " + strings.Join(hosts, ", ")
		s.Logger.PrintBanner(banner, bannerMessage)

	}

	s.Plugins.DoPostListen(s)

	go func() { s.Available <- true }()
	ch := make(chan os.Signal)
	<-ch
	s.Close() // btw, don't panic here

	return nil
}

// Must panics on error, it panics on registed iris' logger
func Must(err error) {
	Default.Must(err)
}

// Must panics on error, it panics on registed iris' logger
func (s *Framework) Must(err error) {
	if err != nil {
		s.Logger.Panic(err.Error())
	}
}

// AddServer same as .Servers.Add(config.Server) instead
//
// AddServers starts a server which listens to this station
// Note that  the view engine's functions {{ url }} and {{ urlpath }} will return the first's registered server's scheme (http/https)
//
// this is useful mostly when you want to have two or more listening ports ( two or more servers ) for the same station
//
// receives one parameter which is the config.Server for the new server
// returns the new standalone server(  you can close this server by the returning reference)
//
// If you need only one server you can use the blocking-funcs: .Listen/ListenTLS/ListenUNIX/ListenTo
//
// this is a NOT A BLOCKING version, the main .Listen/ListenTLS/ListenUNIX/ListenTo should be always executed LAST, so this function goes before the main .Listen/ListenTLS/ListenUNIX/ListenTo
func AddServer(cfg config.Server) *Server {
	return Default.AddServer(cfg)
}

// AddServer same as .Servers.Add(config.Server) instead
//
// AddServers starts a server which listens to this station
// Note that  the view engine's functions {{ url }} and {{ urlpath }} will return the first's registered server's scheme (http/https)
//
// this is useful mostly when you want to have two or more listening ports ( two or more servers ) for the same station
//
// receives one parameter which is the config.Server for the new server
// returns the new standalone server(  you can close this server by the returning reference)
//
// If you need only one server you can use the blocking-funcs: .Listen/ListenTLS/ListenUNIX/ListenTo
//
// this is a NOT A BLOCKING version, the main .Listen/ListenTLS/ListenUNIX/ListenTo should be always executed LAST, so this function goes before the main .Listen/ListenTLS/ListenUNIX/ListenTo
func (s *Framework) AddServer(cfg config.Server) *Server {
	return s.Servers.Add(cfg)
}

// ListenTo listens to a server but receives the full server's configuration
// returns an error, you're responsible to handle that
// or use the iris.Must(iris.ListenTo(config.Server{}))
//
// it's a blocking func
func ListenTo(cfg config.Server) error {
	return Default.ListenTo(cfg)
}

// ListenTo listens to a server but receives the full server's configuration
// it's a blocking func
func (s *Framework) ListenTo(cfg config.Server) (err error) {
	if cfg.ReadBufferSize == 0 {
		cfg.ReadBufferSize = config.DefaultReadBufferSize
	}
	if cfg.WriteBufferSize == 0 {
		cfg.WriteBufferSize = config.DefaultWriteBufferSize
	}
	if cfg.MaxRequestBodySize == 0 {
		cfg.MaxRequestBodySize = config.DefaultMaxRequestBodySize
	}
	if cfg.ListeningAddr == "" {
		cfg.ListeningAddr = config.DefaultServerAddr
	}
	s.Servers.Add(cfg)
	return s.Go()
}

// Listen starts the standalone http server
// which listens to the addr parameter which as the form of
// host:port
//
// It panics on error if you need a func to return an error, use the ListenTo
// ex: err := iris.ListenTo(config.Server{ListeningAddr:":8080"})
func Listen(addr string) {
	Default.Listen(addr)
}

// Listen starts the standalone http server
// which listens to the addr parameter which as the form of
// host:port
//
// It panics on error if you need a func to return an error, use the ListenTo
// ex: err := iris.ListenTo(config.Server{ListeningAddr:":8080"})
func (s *Framework) Listen(addr string) {
	s.Must(s.ListenTo(config.Server{ListeningAddr: addr}))
}

// ListenTLS Starts a https server with certificates,
// if you use this method the requests of the form of 'http://' will fail
// only https:// connections are allowed
// which listens to the addr parameter which as the form of
// host:port
//
// It panics on error if you need a func to return an error, use the ListenTo
// ex: err := iris.ListenTo(":8080","yourfile.cert","yourfile.key")
func ListenTLS(addr string, certFile string, keyFile string) {
	Default.ListenTLS(addr, certFile, keyFile)
}

// ListenTLS Starts a https server with certificates,
// if you use this method the requests of the form of 'http://' will fail
// only https:// connections are allowed
// which listens to the addr parameter which as the form of
// host:port
//
// It panics on error if you need a func to return an error, use the ListenTo
// ex: err := iris.ListenTo(":8080","yourfile.cert","yourfile.key")
func (s *Framework) ListenTLS(addr string, certFile, keyFile string) {
	if certFile == "" || keyFile == "" {
		s.Logger.Panic("You should provide certFile and keyFile for TLS/SSL")
	}
	s.Must(s.ListenTo(config.Server{ListeningAddr: addr, CertFile: certFile, KeyFile: keyFile}))
}

// ListenUNIX starts the process of listening to the new requests using a 'socket file', this works only on unix
//
// It panics on error if you need a func to return an error, use the ListenTo
// ex: err := iris.ListenTo(":8080", Mode: os.FileMode)
func ListenUNIX(addr string, mode os.FileMode) {
	Default.ListenUNIX(addr, mode)
}

// ListenUNIX starts the process of listening to the new requests using a 'socket file', this works only on unix
//
// It panics on error if you need a func to return an error, use the ListenTo
// ex: err := iris.ListenTo(":8080", Mode: os.FileMode)
func (s *Framework) ListenUNIX(addr string, mode os.FileMode) {
	s.Must(ListenTo(config.Server{ListeningAddr: addr, Mode: mode}))
}

// ListenVirtual is useful only when you want to test Iris, it doesn't starts the server but it configures and returns it
// initializes the whole framework but server doesn't listens to a specific net.Listener
// it is not blocking the app
func ListenVirtual(optionalAddr ...string) *Server {
	return Default.ListenVirtual(optionalAddr...)
}

// ListenVirtual is useful only when you want to test Iris, it doesn't starts the server but it configures and returns it
// initializes the whole framework but server doesn't listens to a specific net.Listener
// it is not blocking the app
func (s *Framework) ListenVirtual(optionalAddr ...string) *Server {
	s.Config.DisableBanner = true
	cfg := config.DefaultServer()

	if len(optionalAddr) > 0 && optionalAddr[0] != "" {
		cfg.ListeningAddr = optionalAddr[0]
	}
	cfg.Virtual = true

	go func() {
		s.Must(s.ListenTo(cfg))
	}()

	if ok := <-s.Available; !ok {
		s.Logger.Panic("Unexpected error:Virtual server cannot start, please report this as bug!!")
	}

	close(s.Available)
	return s.Servers.Main()
}

// Close terminates all the registered servers and returns an error if any
// if you want to panic on this error use the iris.Must(iris.Close())
func Close() error {
	return Default.Close()
}

// Close terminates all the registered servers and returns an error if any
// if you want to panic on this error use the iris.Must(iris.Close())
func (s *Framework) Close() error {
	s.Plugins.DoPreClose(s)
	s.Available = make(chan bool)
	return s.Servers.CloseAll()
}

// UseSessionDB registers a session database, you can register more than one
// accepts a session database which implements a Load(sid string) map[string]interface{} and an Update(sid string, newValues map[string]interface{})
// the only reason that a session database will be useful for you is when you want to keep the session's values/data after the app restart
// a session database doesn't have write access to the session, it doesn't accept the context, so forget 'cookie database' for sessions, I will never allow that, for your protection.
//
// Note: Don't worry if no session database is registered, your context.Session will continue to work.
func UseSessionDB(db SessionDatabase) {
	Default.UseSessionDB(db)
}

// UseSessionDB registers a session database, you can register more than one
// accepts a session database which implements a Load(sid string) map[string]interface{} and an Update(sid string, newValues map[string]interface{})
// the only reason that a session database will be useful for you is when you want to keep the session's values/data after the app restart
// a session database doesn't have write access to the session, it doesn't accept the context, so forget 'cookie database' for sessions, I will never allow that, for your protection.
//
// Note: Don't worry if no session database is registered, your context.Session will continue to work.
func (s *Framework) UseSessionDB(db SessionDatabase) {
	s.sessions.provider.registerDatabase(db)
}

// UseTemplate adds a template engine to the iris view system
// it does not build/load them yet
func UseTemplate(e TemplateEngine) *TemplateEngineLocation {
	return Default.UseTemplate(e)
}

// UseTemplate adds a template engine to the iris view system
// it does not build/load them yet
func (s *Framework) UseTemplate(e TemplateEngine) *TemplateEngineLocation {
	return s.templates.Add(e)
}

// UseGlobal registers Handler middleware  to the beginning, prepends them instead of append
//
// Use it when you want to add a global middleware to all parties, to all routes in  all subdomains
// It can be called after other, (but before .Listen of course)
func UseGlobal(handlers ...Handler) {
	Default.UseGlobal(handlers...)
}

// UseGlobalFunc registers HandlerFunc middleware  to the beginning, prepends them instead of append
//
// Use it when you want to add a global middleware to all parties, to all routes in  all subdomains
// It can be called after other, (but before .Listen of course)
func UseGlobalFunc(handlersFn ...HandlerFunc) {
	Default.UseGlobalFunc(handlersFn...)
}

// UseGlobal registers Handler middleware  to the beginning, prepends them instead of append
//
// Use it when you want to add a global middleware to all parties, to all routes in  all subdomains
// It can be called after other, (but before .Listen of course)
func (s *Framework) UseGlobal(handlers ...Handler) {
	for _, r := range s.mux.lookups {
		r.middleware = append(handlers, r.middleware...)
	}
}

// UseGlobalFunc registers HandlerFunc middleware to the beginning, prepends them instead of append
//
// Use it when you want to add a global middleware to all parties, to all routes in  all subdomains
// It can be called after other, (but before .Listen of course)
func (s *Framework) UseGlobalFunc(handlersFn ...HandlerFunc) {
	s.UseGlobal(convertToHandlers(handlersFn)...)
}

// OnError registers a custom http error handler
func OnError(statusCode int, handlerFn HandlerFunc) {
	Default.OnError(statusCode, handlerFn)
}

// EmitError fires a custom http error handler to the client
//
// if no custom error defined with this statuscode, then iris creates one, and once at runtime
func EmitError(statusCode int, ctx *Context) {
	Default.EmitError(statusCode, ctx)
}

// OnError registers a custom http error handler
func (s *Framework) OnError(statusCode int, handlerFn HandlerFunc) {
	s.mux.registerError(statusCode, handlerFn)
}

// EmitError fires a custom http error handler to the client
//
// if no custom error defined with this statuscode, then iris creates one, and once at runtime
func (s *Framework) EmitError(statusCode int, ctx *Context) {
	s.mux.fireError(statusCode, ctx)
}

// Lookup returns a registed route by its name
func Lookup(routeName string) Route {
	return Default.Lookup(routeName)
}

// Lookups returns all registed routes
func Lookups() []Route {
	return Default.Lookups()
}

// Lookup returns a registed route by its name
func (s *Framework) Lookup(routeName string) Route {
	return s.mux.lookup(routeName)
}

// Lookups returns all registed routes
func (s *Framework) Lookups() (routes []Route) {
	// silly but...
	for i := range s.mux.lookups {
		routes = append(routes, s.mux.lookups[i])
	}
	return
}

// Path used to check arguments with the route's named parameters and return the correct url
// if parse failed returns empty string
func Path(routeName string, args ...interface{}) string {
	return Default.Path(routeName, args...)
}

// Path used to check arguments with the route's named parameters and return the correct url
// if parse failed returns empty string
func (s *Framework) Path(routeName string, args ...interface{}) string {
	r := s.mux.lookup(routeName)
	if r == nil {
		return ""
	}

	argsLen := len(args)

	// we have named parameters but arguments not given
	if argsLen == 0 && r.formattedParts > 0 {
		return ""
	} else if argsLen == 0 && r.formattedParts == 0 {
		// it's static then just return the path
		return r.path
	}

	// we have arguments but they are much more than the named parameters

	// 1 check if we have /*, if yes then join all arguments to one as path and pass that as parameter
	if argsLen > r.formattedParts {
		if r.path[len(r.path)-1] == matchEverythingByte {
			// we have to convert each argument to a string in this case

			argsString := make([]string, argsLen, argsLen)

			for i, v := range args {
				if s, ok := v.(string); ok {
					argsString[i] = s
				} else if num, ok := v.(int); ok {
					argsString[i] = strconv.Itoa(num)
				} else if b, ok := v.(bool); ok {
					argsString[i] = strconv.FormatBool(b)
				} else if arr, ok := v.([]string); ok {
					if len(arr) > 0 {
						argsString[i] = arr[0]
						argsString = append(argsString, arr[1:]...)
					}
				}
			}

			parameter := strings.Join(argsString, slash)
			result := fmt.Sprintf(r.formattedPath, parameter)
			return result
		}
		// 2 if !1 return false
		return ""
	}

	arguments := args[0:]

	// check for arrays
	for i, v := range arguments {
		if arr, ok := v.([]string); ok {
			if len(arr) > 0 {
				interfaceArr := make([]interface{}, len(arr))
				for j, sv := range arr {
					interfaceArr[j] = sv
				}
				arguments[i] = interfaceArr[0]
				arguments = append(arguments, interfaceArr[1:]...)
			}

		}
	}

	return fmt.Sprintf(r.formattedPath, arguments...)
}

// URL returns the subdomain+ host + Path(...optional named parameters if route is dynamic)
// returns an empty string if parse is failed
func URL(routeName string, args ...interface{}) (url string) {
	return Default.URL(routeName, args...)
}

// URL returns the subdomain+ host + Path(...optional named parameters if route is dynamic)
// returns an empty string if parse is failed
func (s *Framework) URL(routeName string, args ...interface{}) (url string) {
	r := s.mux.lookup(routeName)
	if r == nil {
		return
	}
	srv := s.Servers.Main()
	scheme := "http://"
	if srv.IsSecure() {
		scheme = "https://"
	}

	host := srv.Host()
	arguments := args[0:]

	// join arrays as arguments
	for i, v := range arguments {
		if arr, ok := v.([]string); ok {
			if len(arr) > 0 {
				interfaceArr := make([]interface{}, len(arr))
				for j, sv := range arr {
					interfaceArr[j] = sv
				}
				arguments[i] = interfaceArr[0]
				arguments = append(arguments, interfaceArr[1:]...)
			}

		}
	}

	// if it's dynamic subdomain then the first argument is the subdomain part
	if r.subdomain == dynamicSubdomainIndicator {
		if len(arguments) == 0 { // it's a wildcard subdomain but not arguments
			return
		}

		if subdomain, ok := arguments[0].(string); ok {
			host = subdomain + "." + host
		} else {
			// it is not array because we join them before. if not pass a string then this is not a subdomain part, return empty uri
			return
		}

		arguments = arguments[1:]
	}

	if parsedPath := s.Path(routeName, arguments...); parsedPath != "" {
		url = scheme + host + parsedPath
	}

	return
}

// TemplateString executes a template from the default template engine and returns its result as string, useful when you want it for sending rich e-mails
// returns empty string on error
func TemplateString(templateFile string, pageContext interface{}, options ...map[string]interface{}) string {
	return Default.TemplateString(templateFile, pageContext, options...)
}

// TemplateString executes a template from the default template engine and returns its result as string, useful when you want it for sending rich e-mails
// returns empty string on error
func (s *Framework) TemplateString(templateFile string, pageContext interface{}, options ...map[string]interface{}) string {
	if s.Config.DisableTemplateEngines {
		return ""
	}
	res, err := s.templates.GetBy(templateFile).ExecuteToString(templateFile, pageContext, options...)
	if err != nil {
		return ""
	}
	return res
}

// NewTester Prepares and returns a new test framework based on the api
// is useful when you need to have more than one test framework for the same iris insttance, otherwise you can use the iris.Tester(t *testing.T)/variable.Tester(t *testing.T)
func NewTester(api *Framework, t *testing.T) *httpexpect.Expect {
	srv := api.Servers.Main()
	if srv == nil { // maybe the user called this after .Listen/ListenTLS/ListenUNIX, the tester can be used as standalone (with no running iris instance) or inside a running instance/app
		srv = api.ListenVirtual(api.Config.Tester.ListeningAddr)
	}

	opened := api.Servers.GetAllOpened()
	h := srv.Handler
	baseURL := srv.FullHost()
	if len(opened) > 1 {
		baseURL = ""
		//we have more than one server, so we will create a handler here and redirect by registered listening addresses
		h = func(reqCtx *fasthttp.RequestCtx) {
			for _, s := range opened {
				if strings.HasPrefix(reqCtx.URI().String(), s.FullHost()) { // yes on :80 should be passed :80 also, this is inneed for multiserver testing
					s.Handler(reqCtx)
					break
				}
			}
		}
	}

	if api.Config.Tester.ExplicitURL {
		baseURL = ""
	}

	testConfiguration := httpexpect.Config{
		BaseURL: baseURL,
		Client: &http.Client{
			Transport: httpexpect.NewFastBinder(h),
			Jar:       httpexpect.NewJar(),
		},
		Reporter: httpexpect.NewAssertReporter(t),
	}

	if api.Config.Tester.Debug {
		testConfiguration.Printers = []httpexpect.Printer{
			httpexpect.NewDebugPrinter(t, true),
		}
	}

	return httpexpect.WithConfig(testConfiguration)
}

// Tester returns the test framework for this default insance
func Tester(t *testing.T) *httpexpect.Expect {
	return Default.Tester(t)
}

// Tester returns the test framework for this iris insance
func (s *Framework) Tester(t *testing.T) *httpexpect.Expect {
	if s.testFramework == nil {
		s.testFramework = NewTester(s, t)
	}
	return s.testFramework
}

// -------------------------------------------------------------------------------------
// -------------------------------------------------------------------------------------
// ----------------------------------MuxAPI implementation------------------------------
// -------------------------------------------------------------------------------------
// -------------------------------------------------------------------------------------
type (
	// RouteNameFunc the func returns from the MuxAPi's methods, optionally sets the name of the Route (*route)
	RouteNameFunc func(string)
	// MuxAPI the visible api for the serveMux
	MuxAPI interface {
		Party(string, ...HandlerFunc) MuxAPI
		// middleware serial, appending
		Use(...Handler)
		UseFunc(...HandlerFunc)

		// main handlers
		Handle(string, string, ...Handler) RouteNameFunc
		HandleFunc(string, string, ...HandlerFunc) RouteNameFunc
		// H_ is used to convert a context.IContext handler func to iris.HandlerFunc, is used only inside iris internal package to avoid import cycles
		H_(string, string, func(context.IContext)) func(string)
		API(string, HandlerAPI, ...HandlerFunc)

		// http methods
		Get(string, ...HandlerFunc) RouteNameFunc
		Post(string, ...HandlerFunc) RouteNameFunc
		Put(string, ...HandlerFunc) RouteNameFunc
		Delete(string, ...HandlerFunc) RouteNameFunc
		Connect(string, ...HandlerFunc) RouteNameFunc
		Head(string, ...HandlerFunc) RouteNameFunc
		Options(string, ...HandlerFunc) RouteNameFunc
		Patch(string, ...HandlerFunc) RouteNameFunc
		Trace(string, ...HandlerFunc) RouteNameFunc
		Any(string, ...HandlerFunc)

		// static content
		StaticHandler(string, int, bool, bool, []string) HandlerFunc
		Static(string, string, int) RouteNameFunc
		StaticFS(string, string, int) RouteNameFunc
		StaticWeb(string, string, int) RouteNameFunc
		StaticServe(string, ...string) RouteNameFunc
		StaticContent(string, string, []byte) func(string)
		Favicon(string, ...string) RouteNameFunc

		// templates
		Layout(string) MuxAPI // returns itself
	}

	muxAPI struct {
		mux          *serveMux
		relativePath string
		middleware   Middleware
	}
)

var _ MuxAPI = &muxAPI{}

var (
	// errAPIContextNotFound returns an error with message: 'From .API: "Context *iris.Context could not be found..'
	errAPIContextNotFound = errors.New("From .API: Context *iris.Context could not be found.")
	// errDirectoryFileNotFound returns an error with message: 'Directory or file %s couldn't found. Trace: +error trace'
	errDirectoryFileNotFound = errors.New("Directory or file %s couldn't found. Trace: %s")
)

// Party is just a group joiner of routes which have the same prefix and share same middleware(s) also.
// Party can also be named as 'Join' or 'Node' or 'Group' , Party chosen because it has more fun
func Party(relativePath string, handlersFn ...HandlerFunc) MuxAPI {
	return Default.Party(relativePath, handlersFn...)
}

// Party is just a group joiner of routes which have the same prefix and share same middleware(s) also.
// Party can also be named as 'Join' or 'Node' or 'Group' , Party chosen because it has more fun
func (api *muxAPI) Party(relativePath string, handlersFn ...HandlerFunc) MuxAPI {
	parentPath := api.relativePath
	dot := string(subdomainIndicator[0])
	if len(parentPath) > 0 && parentPath[0] == slashByte && strings.HasSuffix(relativePath, dot) { // if ends with . , example: admin., it's subdomain->
		parentPath = parentPath[1:] // remove first slash
	}

	fullpath := parentPath + relativePath
	middleware := convertToHandlers(handlersFn)
	// append the parent's +child's handlers
	middleware = joinMiddleware(api.middleware, middleware)
	return &muxAPI{relativePath: fullpath, mux: api.mux, middleware: middleware}
}

// Use registers Handler middleware
func Use(handlers ...Handler) {
	Default.Use(handlers...)
}

// UseFunc registers HandlerFunc middleware
func UseFunc(handlersFn ...HandlerFunc) {
	Default.UseFunc(handlersFn...)
}

// Use registers Handler middleware
func (api *muxAPI) Use(handlers ...Handler) {
	api.middleware = append(api.middleware, handlers...)
}

// UseFunc registers HandlerFunc middleware
func (api *muxAPI) UseFunc(handlersFn ...HandlerFunc) {
	api.Use(convertToHandlers(handlersFn)...)
}

// Handle registers a route to the server's router
// if empty method is passed then registers handler(s) for all methods, same as .Any, but returns nil as result
func Handle(method string, registedPath string, handlers ...Handler) RouteNameFunc {
	return Default.Handle(method, registedPath, handlers...)
}

// HandleFunc registers and returns a route with a method string, path string and a handler
// registedPath is the relative url path
func HandleFunc(method string, registedPath string, handlersFn ...HandlerFunc) RouteNameFunc {
	return Default.HandleFunc(method, registedPath, handlersFn...)
}

// Handle registers a route to the server's router
// if empty method is passed then registers handler(s) for all methods, same as .Any, but returns nil as result
func (api *muxAPI) Handle(method string, registedPath string, handlers ...Handler) RouteNameFunc {
	if method == "" { // then use like it was .Any
		for _, k := range AllMethods {
			api.Handle(k, registedPath, handlers...)
		}
		return nil
	}

	fullpath := api.relativePath + registedPath // keep the last "/" if any,  "/xyz/"

	middleware := joinMiddleware(api.middleware, handlers)

	// here we separate the subdomain and relative path
	subdomain := ""
	path := fullpath

	if dotWSlashIdx := strings.Index(path, subdomainIndicator); dotWSlashIdx > 0 {
		subdomain = fullpath[0 : dotWSlashIdx+1] // admin.
		path = fullpath[dotWSlashIdx+1:]         // /
	}

	path = strings.Replace(path, "//", "/", -1) // fix the path if double //

	return api.mux.register([]byte(method), subdomain, path, middleware).setName
}

// HandleFunc registers and returns a route with a method string, path string and a handler
// registedPath is the relative url path
func (api *muxAPI) HandleFunc(method string, registedPath string, handlersFn ...HandlerFunc) RouteNameFunc {
	return api.Handle(method, registedPath, convertToHandlers(handlersFn)...)
}

// H_ is used to convert a context.IContext handler func to iris.HandlerFunc, is used only inside iris internal package to avoid import cycles
func (api *muxAPI) H_(method string, registedPath string, fn func(context.IContext)) func(string) {
	return api.HandleFunc(method, registedPath, func(ctx *Context) {
		fn(ctx)
	})
}

// API converts & registers a custom struct to the router
// receives two parameters
// first is the request path (string)
// second is the custom struct (interface{}) which can be anything that has a *iris.Context as field.
// third is the common middlewares, it's optional
//
// Note that API's routes have their default-name to the full registed path,
// no need to give a special name for it, because it's not supposed to be used inside your templates.
//
// Recommend to use when you retrieve data from an external database,
// and the router-performance is not the (only) thing which slows the server's overall performance.
//
// This is a slow method, if you care about router-performance use the Handle/HandleFunc/Get/Post/Put/Delete/Trace/Options... instead
func API(path string, restAPI HandlerAPI, middleware ...HandlerFunc) {
	Default.API(path, restAPI, middleware...)
}

// API converts & registers a custom struct to the router
// receives two parameters
// first is the request path (string)
// second is the custom struct (interface{}) which can be anything that has a *iris.Context as field.
// third is the common middleware, it's optional
//
// Note that API's routes have their default-name to the full registed path,
// no need to give a special name for it, because it's not supposed to be used inside your templates.
//
// Recommend to use when you retrieve data from an external database,
// and the router-performance is not the (only) thing which slows the server's overall performance.
//
// This is a slow method, if you care about router-performance use the Handle/HandleFunc/Get/Post/Put/Delete/Trace/Options... instead
func (api *muxAPI) API(path string, restAPI HandlerAPI, middleware ...HandlerFunc) {
	// here we need to find the registed methods and convert them to handler funcs
	// methods are collected by method naming:  Get(),GetBy(...), Post(),PostBy(...), Put() and so on

	typ := reflect.ValueOf(restAPI).Type()
	contextField, found := typ.FieldByName("Context")
	if !found {
		panic(errAPIContextNotFound.Return())
	}

	// check & register the Get(),Post(),Put(),Delete() and so on
	for _, methodName := range AllMethods {

		methodCapitalName := strings.Title(strings.ToLower(methodName))

		if method, found := typ.MethodByName(methodCapitalName); found {
			methodFunc := method.Func
			if !methodFunc.IsValid() || methodFunc.Type().NumIn() > 1 { // for any case
				continue
			}

			func(path string, typ reflect.Type, contextField reflect.StructField, methodFunc reflect.Value, method string) {
				var handlersFn []HandlerFunc

				handlersFn = append(handlersFn, middleware...)
				handlersFn = append(handlersFn, func(ctx *Context) {
					newController := reflect.New(typ).Elem()
					newController.FieldByName("Context").Set(reflect.ValueOf(ctx))
					methodFunc.Call([]reflect.Value{newController})
				})
				// register route
				api.HandleFunc(method, path, handlersFn...)
			}(path, typ, contextField, methodFunc, methodName)

		}

	}

	// check for GetBy/PostBy(id string, something_else string) , these must be requested by the same order.
	// (we could do this in the same top loop but I don't want)
	// GET, DELETE -> with url named parameters (/users/:id/:secondArgumentIfExists)
	// POST, PUT -> with post values (form)
	// all other with URL Parameters (?something=this&else=other
	//
	// or no, I changed my mind, let all be named parameters and let users to decide what info they need,
	// using the Context to take more values (post form,url params and so on).-

	for _, methodName := range AllMethods {
		methodWithBy := strings.Title(strings.ToLower(methodName)) + "By"
		if method, found := typ.MethodByName(methodWithBy); found {
			methodFunc := method.Func
			if !methodFunc.IsValid() || methodFunc.Type().NumIn() < 2 { //it's By but it has not receive any arguments so its not api's
				continue
			}
			methodFuncType := methodFunc.Type()
			numInLen := methodFuncType.NumIn() // how much data we should receive from the request
			registedPath := path

			for i := 1; i < numInLen; i++ { // from 1 because the first is the 'object'
				if registedPath[len(registedPath)-1] == slashByte {
					registedPath += ":param" + strconv.Itoa(i)
				} else {
					registedPath += "/:param" + strconv.Itoa(i)
				}
			}

			func(registedPath string, typ reflect.Type, contextField reflect.StructField, methodFunc reflect.Value, paramsLen int, method string) {
				var handlersFn []HandlerFunc

				handlersFn = append(handlersFn, middleware...)
				handlersFn = append(handlersFn, func(ctx *Context) {
					newController := reflect.New(typ).Elem()
					newController.FieldByName("Context").Set(reflect.ValueOf(ctx))
					args := make([]reflect.Value, paramsLen+1, paramsLen+1)
					args[0] = newController
					for i := 0; i < paramsLen; i++ {
						args[i+1] = reflect.ValueOf(ctx.Params[i].Value)
					}
					methodFunc.Call(args)
				})
				// register route
				api.HandleFunc(method, registedPath, handlersFn...)
			}(registedPath, typ, contextField, methodFunc, numInLen-1, methodName)

		}

	}

}

// Get registers a route for the Get http method
func Get(path string, handlersFn ...HandlerFunc) RouteNameFunc {
	return Default.Get(path, handlersFn...)
}

// Post registers a route for the Post http method
func Post(path string, handlersFn ...HandlerFunc) RouteNameFunc {
	return Default.Post(path, handlersFn...)
}

// Put registers a route for the Put http method
func Put(path string, handlersFn ...HandlerFunc) RouteNameFunc {
	return Default.Put(path, handlersFn...)
}

// Delete registers a route for the Delete http method
func Delete(path string, handlersFn ...HandlerFunc) RouteNameFunc {
	return Default.Delete(path, handlersFn...)
}

// Connect registers a route for the Connect http method
func Connect(path string, handlersFn ...HandlerFunc) RouteNameFunc {
	return Default.Connect(path, handlersFn...)
}

// Head registers a route for the Head http method
func Head(path string, handlersFn ...HandlerFunc) RouteNameFunc {
	return Default.Head(path, handlersFn...)
}

// Options registers a route for the Options http method
func Options(path string, handlersFn ...HandlerFunc) RouteNameFunc {
	return Default.Options(path, handlersFn...)
}

// Patch registers a route for the Patch http method
func Patch(path string, handlersFn ...HandlerFunc) RouteNameFunc {
	return Default.Patch(path, handlersFn...)
}

// Trace registers a route for the Trace http method
func Trace(path string, handlersFn ...HandlerFunc) RouteNameFunc {
	return Default.Trace(path, handlersFn...)
}

// Any registers a route for ALL of the http methods (Get,Post,Put,Head,Patch,Options,Connect,Delete)
func Any(registedPath string, handlersFn ...HandlerFunc) {
	Default.Any(registedPath, handlersFn...)

}

// Get registers a route for the Get http method
func (api *muxAPI) Get(path string, handlersFn ...HandlerFunc) RouteNameFunc {
	return api.HandleFunc(MethodGet, path, handlersFn...)
}

// Post registers a route for the Post http method
func (api *muxAPI) Post(path string, handlersFn ...HandlerFunc) RouteNameFunc {
	return api.HandleFunc(MethodPost, path, handlersFn...)
}

// Put registers a route for the Put http method
func (api *muxAPI) Put(path string, handlersFn ...HandlerFunc) RouteNameFunc {
	return api.HandleFunc(MethodPut, path, handlersFn...)
}

// Delete registers a route for the Delete http method
func (api *muxAPI) Delete(path string, handlersFn ...HandlerFunc) RouteNameFunc {
	return api.HandleFunc(MethodDelete, path, handlersFn...)
}

// Connect registers a route for the Connect http method
func (api *muxAPI) Connect(path string, handlersFn ...HandlerFunc) RouteNameFunc {
	return api.HandleFunc(MethodConnect, path, handlersFn...)
}

// Head registers a route for the Head http method
func (api *muxAPI) Head(path string, handlersFn ...HandlerFunc) RouteNameFunc {
	return api.HandleFunc(MethodHead, path, handlersFn...)
}

// Options registers a route for the Options http method
func (api *muxAPI) Options(path string, handlersFn ...HandlerFunc) RouteNameFunc {
	return api.HandleFunc(MethodOptions, path, handlersFn...)
}

// Patch registers a route for the Patch http method
func (api *muxAPI) Patch(path string, handlersFn ...HandlerFunc) RouteNameFunc {
	return api.HandleFunc(MethodPatch, path, handlersFn...)
}

// Trace registers a route for the Trace http method
func (api *muxAPI) Trace(path string, handlersFn ...HandlerFunc) RouteNameFunc {
	return api.HandleFunc(MethodTrace, path, handlersFn...)
}

// Any registers a route for ALL of the http methods (Get,Post,Put,Head,Patch,Options,Connect,Delete)
func (api *muxAPI) Any(registedPath string, handlersFn ...HandlerFunc) {
	for _, k := range AllMethods {
		api.HandleFunc(k, registedPath, handlersFn...)
	}
}

// StaticHandler returns a Handler to serve static system directory
// Accepts 5 parameters
//
// first is the systemPath (string)
// Path to the root directory to serve files from.
//
// second is the  stripSlashes (int) level
// * stripSlashes = 0, original path: "/foo/bar", result: "/foo/bar"
// * stripSlashes = 1, original path: "/foo/bar", result: "/bar"
// * stripSlashes = 2, original path: "/foo/bar", result: ""
//
// third is the compress (bool)
// Transparently compresses responses if set to true.
//
// The server tries minimizing CPU usage by caching compressed files.
// It adds fasthttp.FSCompressedFileSuffix suffix to the original file name and
// tries saving the resulting compressed file under the new file name.
// So it is advisable to give the server write access to Root
// and to all inner folders in order to minimze CPU usage when serving
// compressed responses.
//
// fourth is the generateIndexPages (bool)
// Index pages for directories without files matching IndexNames
// are automatically generated if set.
//
// Directory index generation may be quite slow for directories
// with many files (more than 1K), so it is discouraged enabling
// index pages' generation for such directories.
//
// fifth is the indexNames ([]string)
// List of index file names to try opening during directory access.
//
// For example:
//
//     * index.html
//     * index.htm
//     * my-super-index.xml
//
func StaticHandler(systemPath string, stripSlashes int, compress bool, generateIndexPages bool, indexNames []string) HandlerFunc {
	return Default.StaticHandler(systemPath, stripSlashes, compress, generateIndexPages, indexNames)
}

// StaticHandler returns a Handler to serve static system directory
// Accepts 5 parameters
//
// first is the systemPath (string)
// Path to the root directory to serve files from.
//
// second is the  stripSlashes (int) level
// * stripSlashes = 0, original path: "/foo/bar", result: "/foo/bar"
// * stripSlashes = 1, original path: "/foo/bar", result: "/bar"
// * stripSlashes = 2, original path: "/foo/bar", result: ""
//
// third is the compress (bool)
// Transparently compresses responses if set to true.
//
// The server tries minimizing CPU usage by caching compressed files.
// It adds fasthttp.FSCompressedFileSuffix suffix to the original file name and
// tries saving the resulting compressed file under the new file name.
// So it is advisable to give the server write access to Root
// and to all inner folders in order to minimze CPU usage when serving
// compressed responses.
//
// fourth is the generateIndexPages (bool)
// Index pages for directories without files matching IndexNames
// are automatically generated if set.
//
// Directory index generation may be quite slow for directories
// with many files (more than 1K), so it is discouraged enabling
// index pages' generation for such directories.
//
// fifth is the indexNames ([]string)
// List of index file names to try opening during directory access.
//
// For example:
//
//     * index.html
//     * index.htm
//     * my-super-index.xml
//
func (api *muxAPI) StaticHandler(systemPath string, stripSlashes int, compress bool, generateIndexPages bool, indexNames []string) HandlerFunc {
	if indexNames == nil {
		indexNames = []string{}
	}
	fs := &fasthttp.FS{
		// Path to directory to serve.
		Root:       systemPath,
		IndexNames: indexNames,
		// Generate index pages if client requests directory contents.
		GenerateIndexPages: generateIndexPages,

		// Enable transparent compression to save network traffic.
		Compress:             compress,
		CacheDuration:        config.StaticCacheDuration,
		CompressedFileSuffix: config.CompressedFileSuffix,
	}

	if stripSlashes > 0 {
		fs.PathRewrite = fasthttp.NewPathSlashesStripper(stripSlashes)
	}

	// Create request handler for serving static files.
	h := fs.NewRequestHandler()
	return HandlerFunc(func(ctx *Context) {
		h(ctx.RequestCtx)
		errCode := ctx.RequestCtx.Response.StatusCode()
		if errCode == StatusNotFound || errCode == StatusBadRequest || errCode == StatusInternalServerError {
			api.mux.fireError(errCode, ctx)
		}
		if ctx.pos < uint8(len(ctx.middleware))-1 {
			ctx.Next() // for any case
		}

	})
}

// Static registers a route which serves a system directory
// this doesn't generates an index page which list all files
// no compression is used also, for these features look at StaticFS func
// accepts three parameters
// first parameter is the request url path (string)
// second parameter is the system directory (string)
// third parameter is the level (int) of stripSlashes
// * stripSlashes = 0, original path: "/foo/bar", result: "/foo/bar"
// * stripSlashes = 1, original path: "/foo/bar", result: "/bar"
// * stripSlashes = 2, original path: "/foo/bar", result: ""
func Static(relative string, systemPath string, stripSlashes int) RouteNameFunc {
	return Default.Static(relative, systemPath, stripSlashes)
}

// Static registers a route which serves a system directory
// this doesn't generates an index page which list all files
// no compression is used also, for these features look at StaticFS func
// accepts three parameters
// first parameter is the request url path (string)
// second parameter is the system directory (string)
// third parameter is the level (int) of stripSlashes
// * stripSlashes = 0, original path: "/foo/bar", result: "/foo/bar"
// * stripSlashes = 1, original path: "/foo/bar", result: "/bar"
// * stripSlashes = 2, original path: "/foo/bar", result: ""
func (api *muxAPI) Static(relative string, systemPath string, stripSlashes int) RouteNameFunc {
	if relative[len(relative)-1] != slashByte { // if / then /*filepath, if /something then /something/*filepath
		relative += slash
	}

	h := api.StaticHandler(systemPath, stripSlashes, false, false, nil)

	api.Head(relative+"*filepath", h)
	return api.Get(relative+"*filepath", h)
}

// StaticFS registers a route which serves a system directory
// this is the fastest method to serve static files
// generates an index page which list all files
// if you use this method it will generate compressed files also
// think this function as small fileserver with http
// accepts three parameters
// first parameter is the request url path (string)
// second parameter is the system directory (string)
// third parameter is the level (int) of stripSlashes
// * stripSlashes = 0, original path: "/foo/bar", result: "/foo/bar"
// * stripSlashes = 1, original path: "/foo/bar", result: "/bar"
// * stripSlashes = 2, original path: "/foo/bar", result: ""
func StaticFS(reqPath string, systemPath string, stripSlashes int) RouteNameFunc {
	return Default.StaticFS(reqPath, systemPath, stripSlashes)
}

// StaticFS registers a route which serves a system directory
// this is the fastest method to serve static files
// generates an index page which list all files
// if you use this method it will generate compressed files also
// think this function as small fileserver with http
// accepts three parameters
// first parameter is the request url path (string)
// second parameter is the system directory (string)
// third parameter is the level (int) of stripSlashes
// * stripSlashes = 0, original path: "/foo/bar", result: "/foo/bar"
// * stripSlashes = 1, original path: "/foo/bar", result: "/bar"
// * stripSlashes = 2, original path: "/foo/bar", result: ""
func (api *muxAPI) StaticFS(reqPath string, systemPath string, stripSlashes int) RouteNameFunc {
	if reqPath[len(reqPath)-1] != slashByte {
		reqPath += slash
	}

	h := api.StaticHandler(systemPath, stripSlashes, true, true, nil)
	api.Head(reqPath+"*filepath", h)
	return api.Get(reqPath+"*filepath", h)
}

// StaticWeb same as Static but if index.html exists and request uri is '/' then display the index.html's contents
// accepts three parameters
// first parameter is the request url path (string)
// second parameter is the system directory (string)
// third parameter is the level (int) of stripSlashes
// * stripSlashes = 0, original path: "/foo/bar", result: "/foo/bar"
// * stripSlashes = 1, original path: "/foo/bar", result: "/bar"
// * stripSlashes = 2, original path: "/foo/bar", result: ""
// * if you don't know what to put on stripSlashes just 1
func StaticWeb(reqPath string, systemPath string, stripSlashes int) RouteNameFunc {
	return Default.StaticWeb(reqPath, systemPath, stripSlashes)
}

// StaticWeb same as Static but if index.html exists and request uri is '/' then display the index.html's contents
// accepts three parameters
// first parameter is the request url path (string)
// second parameter is the system directory (string)
// third parameter is the level (int) of stripSlashes
// * stripSlashes = 0, original path: "/foo/bar", result: "/foo/bar"
// * stripSlashes = 1, original path: "/foo/bar", result: "/bar"
// * stripSlashes = 2, original path: "/foo/bar", result: ""
// * if you don't know what to put on stripSlashes just 1
func (api *muxAPI) StaticWeb(reqPath string, systemPath string, stripSlashes int) RouteNameFunc {
	if reqPath[len(reqPath)-1] != slashByte { // if / then /*filepath, if /something then /something/*filepath
		reqPath += slash
	}

	hasIndex := utils.Exists(systemPath + utils.PathSeparator + "index.html")
	serveHandler := api.StaticHandler(systemPath, stripSlashes, false, !hasIndex, nil) // if not index.html exists then generate index.html which shows the list of files
	indexHandler := func(ctx *Context) {
		if len(ctx.Param("filepath")) < 2 && hasIndex {
			ctx.Request.SetRequestURI("index.html")
		}
		ctx.Next()

	}
	api.Head(reqPath+"*filepath", indexHandler, serveHandler)
	return api.Get(reqPath+"*filepath", indexHandler, serveHandler)
}

// StaticServe serves a directory as web resource
// it's the simpliest form of the Static* functions
// Almost same usage as StaticWeb
// accepts only one required parameter which is the systemPath ( the same path will be used to register the GET&HEAD routes)
// if second parameter is empty, otherwise the requestPath is the second parameter
// it uses gzip compression (compression on each request, no file cache)
func StaticServe(systemPath string, requestPath ...string) RouteNameFunc {
	return Default.StaticServe(systemPath, requestPath...)
}

// StaticServe serves a directory as web resource
// it's the simpliest form of the Static* functions
// Almost same usage as StaticWeb
// accepts only one required parameter which is the systemPath ( the same path will be used to register the GET&HEAD routes)
// if second parameter is empty, otherwise the requestPath is the second parameter
// it uses gzip compression (compression on each request, no file cache)
func (api *muxAPI) StaticServe(systemPath string, requestPath ...string) RouteNameFunc {
	var reqPath string

	if len(requestPath) == 0 {
		reqPath = strings.Replace(systemPath, utils.PathSeparator, slash, -1) // replaces any \ to /
		reqPath = strings.Replace(reqPath, "//", slash, -1)                   // for any case, replaces // to /
		reqPath = strings.Replace(reqPath, ".", "", -1)                       // replace any dots (./mypath -> /mypath)
	} else {
		reqPath = requestPath[0]
	}

	return api.Get(reqPath+"/*file", func(ctx *Context) {
		filepath := ctx.Param("file")

		spath := strings.Replace(filepath, "/", utils.PathSeparator, -1)
		spath = path.Join(systemPath, spath)

		if !utils.DirectoryExists(spath) {
			ctx.NotFound()
			return
		}

		ctx.ServeFile(spath, true)
	})
}

// StaticContent serves bytes, memory cached, on the reqPath
// a good example of this is how the websocket server uses that to auto-register the /iris-ws.js
func StaticContent(reqPath string, contentType string, content []byte) RouteNameFunc {
	return Default.StaticContent(reqPath, contentType, content)
}

// StaticContent serves bytes, memory cached, on the reqPath
// a good example of this is how the websocket server uses that to auto-register the /iris-ws.js
func (api *muxAPI) StaticContent(reqPath string, cType string, content []byte) func(string) { // func(string) because we use that on websockets
	modtime := time.Now()
	modtimeStr := modtime.UTC().Format(config.TimeFormat)
	h := func(ctx *Context) {
		if t, err := time.Parse(config.TimeFormat, ctx.RequestHeader(ifModifiedSince)); err == nil && modtime.Before(t.Add(config.StaticCacheDuration)) {
			ctx.Response.Header.Del(contentType)
			ctx.Response.Header.Del(contentLength)
			ctx.SetStatusCode(StatusNotModified)
			return
		}

		ctx.Response.Header.Set(contentType, cType)
		ctx.Response.Header.Set(lastModified, modtimeStr)
		ctx.SetStatusCode(StatusOK)
		ctx.Response.SetBody(content)
	}
	api.Head(reqPath, h)
	return api.Get(reqPath, h)
}

// Favicon serves static favicon
// accepts 2 parameters, second is optional
// favPath (string), declare the system directory path of the __.ico
// requestPath (string), it's the route's path, by default this is the "/favicon.ico" because some browsers tries to get this by default first,
// you can declare your own path if you have more than one favicon (desktop, mobile and so on)
//
// this func will add a route for you which will static serve the /yuorpath/yourfile.ico to the /yourfile.ico (nothing special that you can't handle by yourself)
// Note that you have to call it on every favicon you have to serve automatically (dekstop, mobile and so on)
//
// panics on error
func Favicon(favPath string, requestPath ...string) RouteNameFunc {
	return Default.Favicon(favPath, requestPath...)
}

// Favicon serves static favicon
// accepts 2 parameters, second is optional
// favPath (string), declare the system directory path of the __.ico
// requestPath (string), it's the route's path, by default this is the "/favicon.ico" because some browsers tries to get this by default first,
// you can declare your own path if you have more than one favicon (desktop, mobile and so on)
//
// this func will add a route for you which will static serve the /yuorpath/yourfile.ico to the /yourfile.ico (nothing special that you can't handle by yourself)
// Note that you have to call it on every favicon you have to serve automatically (dekstop, mobile and so on)
//
// panics on error
func (api *muxAPI) Favicon(favPath string, requestPath ...string) RouteNameFunc {
	f, err := os.Open(favPath)
	if err != nil {
		panic(errDirectoryFileNotFound.Format(favPath, err.Error()))
	}
	defer f.Close()
	fi, _ := f.Stat()
	if fi.IsDir() { // if it's dir the try to get the favicon.ico
		fav := path.Join(favPath, "favicon.ico")
		f, err = os.Open(fav)
		if err != nil {
			//we try again with .png
			return api.Favicon(path.Join(favPath, "favicon.png"))
		}
		favPath = fav
		fi, _ = f.Stat()
	}
	modtime := fi.ModTime().UTC().Format(config.TimeFormat)
	cType := utils.TypeByExtension(favPath)
	// copy the bytes here in order to cache and not read the ico on each request.
	cacheFav := make([]byte, fi.Size())
	if _, err = f.Read(cacheFav); err != nil {
		panic(errDirectoryFileNotFound.Format(favPath, "Couldn't read the data bytes for Favicon: "+err.Error()))
	}

	h := func(ctx *Context) {
		if t, err := time.Parse(config.TimeFormat, ctx.RequestHeader(ifModifiedSince)); err == nil && fi.ModTime().Before(t.Add(config.StaticCacheDuration)) {
			ctx.Response.Header.Del(contentType)
			ctx.Response.Header.Del(contentLength)
			ctx.SetStatusCode(StatusNotModified)
			return
		}

		ctx.Response.Header.Set(contentType, cType)
		ctx.Response.Header.Set(lastModified, modtime)
		ctx.SetStatusCode(StatusOK)
		ctx.Response.SetBody(cacheFav)
	}

	reqPath := "/favicon" + path.Ext(fi.Name()) //we could use the filename, but because standards is /favicon.ico/.png.
	if len(requestPath) > 0 {
		reqPath = requestPath[0]
	}

	api.Head(reqPath, h)
	return api.Get(reqPath, h)
}

// Layout oerrides the parent template layout with a more specific layout for this Party
// returns this Party, to continue as normal
// example:
// my := iris.Party("/my").Layout("layouts/mylayout.html")
// 	{
// 		my.Get("/", func(ctx *iris.Context) {
// 			ctx.MustRender("page1.html", nil)
// 		})
// 	}
//
func (api *muxAPI) Layout(tmplLayoutFile string) MuxAPI {
	api.UseFunc(func(ctx *Context) {
		ctx.Set(TemplateLayoutContextKey, tmplLayoutFile)
		ctx.Next()
	})
	return api
}
