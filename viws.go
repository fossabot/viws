package main

import (
	"bufio"
	"compress/gzip"
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path"
	"regexp"
	"runtime"
	"strings"
	"syscall"
)

const notFoundFilename = `404.html`
const indexFilename = `index.html`
const redirectPrefix = `://www.`
const hostHeader = `X-Forwarded-Host`
const protocolHeader = `X-Forwarded-Proto`

var pngFile = regexp.MustCompile(`.png$`)
var acceptGzip = regexp.MustCompile(`^(?:gzip|\*)(?:;q=(?:1.*?|0\.[1-9][0-9]*))?$`)
var rootDomainMatcher = regexp.MustCompile(`^[^\.]+\.[^\.]+$`)
var requestsHandler = gzipHandler{owaspHandler{customFileHandler{}}}

var directory string
var csp string
var notFound bool
var notFoundPath *string
var spa bool
var redirect bool
var hsts bool

func isFileExist(parts ...string) *string {
	fullPath := path.Join(parts...)
	info, err := os.Stat(fullPath)

	if err != nil {
		log.Print(err)
		return nil
	}

	if info.IsDir() {
		if isFileExist(append(parts, indexFilename)...) == nil {
			return nil
		}
	}

	return &fullPath
}

type gzipMiddleware struct {
	http.ResponseWriter
	gzw *gzip.Writer
}

func (m *gzipMiddleware) WriteHeader(status int) {
	m.ResponseWriter.Header().Add(`Vary`, `Accept-Encoding`)
	m.ResponseWriter.Header().Set(`Content-Encoding`, `gzip`)
	m.ResponseWriter.Header().Del(`Content-Length`)

	if !(http.StatusNotFound == status && notFound) {
		m.ResponseWriter.WriteHeader(status)
	}
}

func (m *gzipMiddleware) Write(b []byte) (int, error) {
	return m.gzw.Write(b)
}

func (m *gzipMiddleware) Flush() {
	m.gzw.Flush()

	if flusher, ok := m.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (m *gzipMiddleware) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if hijacker, ok := m.ResponseWriter.(http.Hijacker); ok {
		return hijacker.Hijack()
	}
	return nil, nil, fmt.Errorf(`http.Hijacker not available`)
}

type gzipHandler struct {
	h http.Handler
}

func (handler gzipHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if acceptEncodingGzip(r) && !pngFile.MatchString(r.URL.Path) {
		gzipWriter := gzip.NewWriter(w)
		defer gzipWriter.Close()

		handler.h.ServeHTTP(&gzipMiddleware{w, gzipWriter}, r)
	} else {
		handler.h.ServeHTTP(w, r)
	}
}

func acceptEncodingGzip(r *http.Request) bool {
	header := r.Header.Get(`Accept-Encoding`)

	for _, headerEncoding := range strings.Split(header, `,`) {
		if acceptGzip.MatchString(headerEncoding) {
			return true
		}
	}

	return false
}

type owaspMiddleware struct {
	http.ResponseWriter
	path string
}

func (m *owaspMiddleware) WriteHeader(status int) {
	if status < http.StatusBadRequest {
		m.Header().Add(`Content-Security-Policy`, csp)
		m.Header().Add(`Referrer-Policy`, `strict-origin-when-cross-origin`)
		m.Header().Add(`X-Frame-Options`, `deny`)
		m.Header().Add(`X-Content-Type-Options`, `nosniff`)
		m.Header().Add(`X-XSS-Protection`, `1; mode=block`)
		m.Header().Add(`X-Permitted-Cross-Domain-Policies`, `none`)
	}

	if hsts {
		m.Header().Add(`Strict-Transport-Security`, `max-age=5184000`)
	}

	if status == http.StatusOK || status == http.StatusMovedPermanently {
		if m.path == `/` {
			m.Header().Add(`Cache-Control`, `no-cache`)
		} else {
			m.Header().Add(`Cache-Control`, `max-age=864000`)
		}
	}

	m.ResponseWriter.WriteHeader(status)
}

type owaspHandler struct {
	h http.Handler
}

func (handler owaspHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	handler.h.ServeHTTP(&owaspMiddleware{w, r.URL.Path}, r)
}

type customFileHandler struct {
}

func (handler customFileHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
	} else if filePath := isFileExist(directory, r.URL.Path); filePath != nil {
		http.ServeFile(w, r, *filePath)
	} else if notFound {
		w.WriteHeader(http.StatusNotFound)
		http.ServeFile(w, r, *notFoundPath)
	} else if spa {
		http.ServeFile(w, r, directory)
	} else {
		http.Error(w, `404 page not found: `+r.URL.Path, http.StatusNotFound)
	}
}

func healthHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		w.WriteHeader(http.StatusOK)
	} else {
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func viwsHandler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == `/health` {
		healthHandler(w, r)
	} else if redirect && rootDomainMatcher.MatchString(r.Header[hostHeader][0]) {
		http.Redirect(w, r, r.Header[protocolHeader][0]+redirectPrefix+rootDomainMatcher.FindStringSubmatch(r.Header[hostHeader][0])[0], http.StatusPermanentRedirect)
	} else {
		requestsHandler.ServeHTTP(w, r)
	}
}

func handleGracefulClose(server *http.Server) {
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGTERM)

	<-signals
	log.Print(`SIGTERM received`)

	if server != nil {
		log.Print(`Shutting down http server`)
		if err := server.Shutdown(context.Background()); err != nil {
			log.Fatal(err)
		}
	}

	os.Exit(0)
}

func main() {
	runtime.GOMAXPROCS(runtime.NumCPU())

	port := flag.String(`port`, `1080`, `Listening port`)
	flag.StringVar(&directory, `directory`, `/www/`, `Directory to serve`)
	flag.BoolVar(&hsts, `hsts`, true, `Indicate Strict Transport Security`)
	flag.BoolVar(&spa, `spa`, false, `Indicate Single Page Application mode`)
	flag.BoolVar(&notFound, `notFound`, false, `Graceful 404 page at /404.html`)
	flag.StringVar(&csp, `csp`, `default-src 'self'`, `Content-Security-Policy`)
	flag.BoolVar(&redirect, `redirect`, false, `Redirect root request (based on X-Forwarded-Host Header) to 'www'`)
	flag.Parse()

	if isFileExist(directory) == nil {
		log.Fatalf(`Directory %s is unreachable`, directory)
	}

	log.Printf(`Starting server on port %s`, *port)
	log.Printf(`Serving file from %s`, directory)
	log.Printf(`Content-Security-Policy: %s`, csp)

	if spa {
		log.Print(`Working in SPA mode`)
	}

	if notFound {
		if notFoundPath = isFileExist(directory, notFoundFilename); notFoundPath == nil {
			log.Printf(`%s%s is unreachable. Not found flag ignored.`, directory, notFoundFilename)
			notFound = false
		} else {
			log.Printf(`404 will be %s`, *notFoundPath)
		}
	}

	server := &http.Server{
		Addr:    `:` + *port,
		Handler: http.HandlerFunc(viwsHandler),
	}

	go handleGracefulClose(server)
	log.Fatal(server.ListenAndServe())
}