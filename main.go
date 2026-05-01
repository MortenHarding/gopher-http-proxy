package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"
	"time"
)

// Gopher item types (RFC 1436)
const (
	TypeFile      = '0'
	TypeDirectory = '1'
	TypeBinary    = '9'
	TypeImage     = 'I'
	TypeGIF       = 'g'
	TypeInfo      = 'i'
	TypePDF       = 'd'
)

// separator used to divide gophermap sections
const sectionSep = "_______________________________"

// Config holds runtime configuration.
type Config struct {
	Host    string
	Port    int
	BaseURL string // used only when no config file is present / single-URL mode
	URLs    []ProxyURL
}

// ProxyURL is one entry from the config file.
type ProxyURL struct {
	Label string // display name shown in the top-level menu
	URL   string // HTTP URL to proxy
}

// DirEntry is a parsed row from an Apache directory listing.
type DirEntry struct {
	Name  string
	URL   string
	IsDir bool
}

// connLogger wraps a standard logger that writes to both stderr and an optional file.
type connLogger struct {
	logger *log.Logger
}

func newConnLogger(logFile string) (*connLogger, error) {
	var w io.Writer = os.Stderr
	if logFile != "" {
		f, err := os.OpenFile(logFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
		if err != nil {
			return nil, fmt.Errorf("opening log file %q: %w", logFile, err)
		}
		w = io.MultiWriter(os.Stderr, f)
	}
	return &connLogger{logger: log.New(w, "", 0)}, nil
}

func (cl *connLogger) logf(format string, a ...any) {
	ts := time.Now().Format("2006-01-02 15:04:05")
	cl.logger.Printf("[%s] "+format, append([]any{ts}, a...)...)
}

// gopherType returns the RFC-1436 item-type byte for a filename.
func gopherType(name string) byte {
	lower := strings.ToLower(name)
	switch {
	case strings.HasSuffix(lower, "/"):
		return TypeDirectory
	case strings.HasSuffix(lower, ".gif"):
		return TypeGIF
	case strings.HasSuffix(lower, ".jpg"), strings.HasSuffix(lower, ".jpeg"),
		strings.HasSuffix(lower, ".png"), strings.HasSuffix(lower, ".bmp"),
		strings.HasSuffix(lower, ".tif"), strings.HasSuffix(lower, ".tiff"):
		return TypeImage
	case strings.HasSuffix(lower, ".txt"), strings.HasSuffix(lower, ".md"),
		strings.HasSuffix(lower, ".log"), strings.HasSuffix(lower, ".cfg"),
		strings.HasSuffix(lower, ".conf"), strings.HasSuffix(lower, ".ini"),
		strings.HasSuffix(lower, ".csv"), strings.HasSuffix(lower, ".nfo"):
		return TypeFile
	case strings.HasSuffix(lower, ".pdf"):
		return TypePDF
	default:
		return TypeBinary
	}
}

// ensureScheme prepends https:// if rawURL has no scheme.
func ensureScheme(rawURL string) string {
	if !strings.HasPrefix(rawURL, "http://") && !strings.HasPrefix(rawURL, "https://") {
		return "https://" + rawURL
	}
	return rawURL
}

// ---- Config file parser ---------------------------------------------------
//
// Format of gopher-proxy.conf:
//
//   # comment lines start with #
//   [header]
//   <any text lines — displayed as Gopher type-i info items at the top>
//
//   [urls]
//   Label | https://example.org/path/
//   Another label | ftp.gnu.org/gnu
//
// A bare URL with no '|' uses the URL itself as the label.
// The [header] section is optional; the [urls] section is mandatory.

const configFile = "gopher-proxy.conf"

type proxyConfig struct {
	HeaderLines []string
	URLs        []ProxyURL
}

func loadConfigFile(filename string) (*proxyConfig, error) {
	f, err := os.Open(filename)
	if err != nil {
		return nil, err // caller treats os.ErrNotExist as "no config file"
	}
	defer f.Close()

	cfg := &proxyConfig{}
	section := ""
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)

		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if trimmed == "[header]" {
			section = "header"
			continue
		}
		if trimmed == "[urls]" {
			section = "urls"
			continue
		}

		switch section {
		case "header":
			cfg.HeaderLines = append(cfg.HeaderLines, line)
		case "urls":
			label := trimmed
			rawURL := trimmed
			if idx := strings.Index(trimmed, "|"); idx >= 0 {
				label = strings.TrimSpace(trimmed[:idx])
				rawURL = strings.TrimSpace(trimmed[idx+1:])
			}
			cfg.URLs = append(cfg.URLs, ProxyURL{
				Label: label,
				URL:   ensureScheme(rawURL),
			})
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("reading %s: %w", filename, err)
	}
	if len(cfg.URLs) == 0 {
		return nil, fmt.Errorf("%s: no [urls] entries found", filename)
	}
	return cfg, nil
}

// writeExampleConfig writes a sample gopher-proxy.conf for the user.
func writeExampleConfig() {
	const example = `# gopher-proxy.conf — example configuration for gopher-http-proxy
#
# Two sections are supported:
#
#   [header]   Lines of ASCII art / info text shown at the top of the
#              generated root gophermap (Gopher type 'i' info items).
#
#   [urls]     One URL per line.  Format:
#                 Display Label | https://host/path/
#              A bare URL with no '|' uses the URL itself as the label.

[header]
  __ _  ___  _ __  | _ __ _ __ _____  ___   _
 / _` + "`" + ` |/ _ \| '_ \ | '_ \| '__/ _ \ \/ / | | |
| (_| | (_) | |_) || |_) | | | (_) >  <| |_| |
 \__, |\___/| .__/ | .__/|_|  \___/_/\_\\__, |
 |___/      |_|    |_|                  |___/

[urls]
Bitsavers PDF archive    | https://bitsavers.org/pdf/
GNU FTP archive          | https://ftp.gnu.org/gnu/
`
	if err := os.WriteFile(configFile+".example", []byte(example), 0644); err != nil {
		log.Printf("Warning: could not write example config: %v", err)
	} else {
		log.Printf("Wrote example config to %s.example", configFile)
	}
}

// ---- Minimal HTML tokenizer (no external deps) ---------------------------

type tokenType int

const (
	tokError tokenType = iota
	tokEOF
	tokStartTag
	tokEndTag
	tokText
)

type token struct {
	Type  tokenType
	Data  string
	Attrs map[string]string
}

type tokenizer struct {
	src string
	pos int
}

func newTokenizer(src string) *tokenizer { return &tokenizer{src: src} }

func (t *tokenizer) next() token {
	if t.pos >= len(t.src) {
		return token{Type: tokEOF}
	}
	if t.src[t.pos] != '<' {
		start := t.pos
		for t.pos < len(t.src) && t.src[t.pos] != '<' {
			t.pos++
		}
		return token{Type: tokText, Data: t.src[start:t.pos]}
	}
	t.pos++ // consume '<'
	if t.pos >= len(t.src) {
		return token{Type: tokEOF}
	}

	if strings.HasPrefix(t.src[t.pos:], "!") || strings.HasPrefix(t.src[t.pos:], "?") {
		for t.pos < len(t.src) && t.src[t.pos] != '>' {
			t.pos++
		}
		if t.pos < len(t.src) {
			t.pos++
		}
		return t.next()
	}

	isEnd := false
	if t.src[t.pos] == '/' {
		isEnd = true
		t.pos++
	}

	nameStart := t.pos
	for t.pos < len(t.src) && t.src[t.pos] != ' ' && t.src[t.pos] != '>' &&
		t.src[t.pos] != '\t' && t.src[t.pos] != '\n' && t.src[t.pos] != '\r' && t.src[t.pos] != '/' {
		t.pos++
	}
	tagName := strings.ToLower(t.src[nameStart:t.pos])

	attrs := map[string]string{}
	for t.pos < len(t.src) && t.src[t.pos] != '>' {
		for t.pos < len(t.src) && (t.src[t.pos] == ' ' || t.src[t.pos] == '\t' ||
			t.src[t.pos] == '\n' || t.src[t.pos] == '\r') {
			t.pos++
		}
		if t.pos >= len(t.src) || t.src[t.pos] == '>' || t.src[t.pos] == '/' {
			break
		}
		aStart := t.pos
		for t.pos < len(t.src) && t.src[t.pos] != '=' && t.src[t.pos] != '>' &&
			t.src[t.pos] != ' ' && t.src[t.pos] != '\t' {
			t.pos++
		}
		attrName := strings.ToLower(t.src[aStart:t.pos])
		attrVal := ""
		if t.pos < len(t.src) && t.src[t.pos] == '=' {
			t.pos++
			if t.pos < len(t.src) {
				if t.src[t.pos] == '"' || t.src[t.pos] == '\'' {
					quote := t.src[t.pos]
					t.pos++
					vStart := t.pos
					for t.pos < len(t.src) && t.src[t.pos] != quote {
						t.pos++
					}
					attrVal = t.src[vStart:t.pos]
					if t.pos < len(t.src) {
						t.pos++
					}
				} else {
					vStart := t.pos
					for t.pos < len(t.src) && t.src[t.pos] != ' ' && t.src[t.pos] != '>' {
						t.pos++
					}
					attrVal = t.src[vStart:t.pos]
				}
			}
		}
		if attrName != "" {
			attrs[attrName] = attrVal
		}
	}
	if t.pos < len(t.src) && t.src[t.pos] == '>' {
		t.pos++
	}

	tt := tokStartTag
	if isEnd {
		tt = tokEndTag
	}
	return token{Type: tt, Data: tagName, Attrs: attrs}
}

// ---- End tokenizer --------------------------------------------------------

// parseApacheIndex fetches an HTTP URL and extracts directory entries.
func parseApacheIndex(rawURL string) (title string, entries []DirEntry, err error) {
	rawURL = ensureScheme(rawURL)

	resp, err := http.Get(rawURL)
	if err != nil {
		return "", nil, fmt.Errorf("HTTP GET failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", nil, fmt.Errorf("HTTP %d from %s", resp.StatusCode, rawURL)
	}

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", nil, fmt.Errorf("reading body: %w", err)
	}

	base, err := url.Parse(rawURL)
	if err != nil {
		return "", nil, err
	}

	tz := newTokenizer(string(bodyBytes))
	inTitle := false

	for {
		tok := tz.next()
		switch tok.Type {
		case tokEOF, tokError:
			return title, entries, nil

		case tokStartTag:
			if tok.Data == "title" {
				inTitle = true
			}
			if tok.Data == "a" {
				href, ok := tok.Attrs["href"]
				if !ok || href == "" {
					continue
				}
				if href == "/" || href == "../" ||
					strings.HasPrefix(href, "?") ||
					strings.HasPrefix(href, "mailto:") {
					continue
				}
				if strings.HasPrefix(href, "http://") || strings.HasPrefix(href, "https://") {
					continue
				}
				ref, parseErr := url.Parse(href)
				if parseErr != nil {
					continue
				}
				resolved := base.ResolveReference(ref)
				isDir := strings.HasSuffix(href, "/")
				entries = append(entries, DirEntry{
					Name:  href,
					URL:   resolved.String(),
					IsDir: isDir,
				})
			}

		case tokText:
			if inTitle {
				title = strings.TrimSpace(tok.Data)
				inTitle = false
			}

		case tokEndTag:
			if tok.Data == "title" {
				inTitle = false
			}
		}
	}
}

// buildGopherMap renders a Gopher menu (RFC 1436) from directory entries.
func buildGopherMap(cfg *Config, title string, entries []DirEntry, httpURL string) string {
	var sb strings.Builder
	none := "none"
	zero := 0

	write := func(format string, a ...any) { fmt.Fprintf(&sb, format, a...) }

	write("i%s\t\t%s\t%d\r\n", title, none, zero)
	write("i%s\t\t%s\t%d\r\n", sectionSep, none, zero)
	write("i\t\t%s\t%d\r\n", none, zero)

	for _, e := range entries {
		if e.IsDir {
			displayName := strings.TrimSuffix(e.Name, "/")
			selector := "/proxy?url=" + url.QueryEscape(e.URL)
			write("%c%s\t%s\t%s\t%d\r\n", TypeDirectory, displayName, selector, cfg.Host, cfg.Port)
		} else {
			displayName := path.Base(e.Name)
			t := gopherType(e.Name)
			selector := "/download?url=" + url.QueryEscape(e.URL)
			write("%c%s\t%s\t%s\t%d\r\n", t, displayName, selector, cfg.Host, cfg.Port)
		}
	}

	write(".\r\n")
	return sb.String()
}

// buildRootGopherMap builds the top-level gophermap from the config file.
// Layout:
//
//	[header lines as type-i info]
//	_______________________________
//	[type-1 directory selectors for each URL]
func buildRootGopherMap(cfg *Config, pcfg *proxyConfig) string {
	var sb strings.Builder
	none := "none"
	zero := 0

	write := func(format string, a ...any) { fmt.Fprintf(&sb, format, a...) }

	// Header section (ASCII art / info text)
	for _, line := range pcfg.HeaderLines {
		write("i%s\t\t%s\t%d\r\n", line, none, zero)
	}

	// Separator
	write("i%s\t\t%s\t%d\r\n", sectionSep, none, zero)
	write("i\t\t%s\t%d\r\n", none, zero)

	// URL selectors
	for _, pu := range pcfg.URLs {
		selector := "/proxy?url=" + url.QueryEscape(pu.URL)
		write("%c%s\t%s\t%s\t%d\r\n", TypeDirectory, pu.Label, selector, cfg.Host, cfg.Port)
	}

	write(".\r\n")
	return sb.String()
}

// handleGopherConn reads the selector from a Gopher client and responds.
func handleGopherConn(conn net.Conn, cfg *Config, pcfg *proxyConfig, cl *connLogger) {
	defer conn.Close()

	remoteAddr := conn.RemoteAddr().String()
	cl.logf("CONNECT %s", remoteAddr)

	buf := make([]byte, 4096)
	n, err := conn.Read(buf)
	if err != nil && err != io.EOF {
		cl.logf("READ ERROR %s: %v", remoteAddr, err)
		return
	}
	selector := strings.TrimRight(string(buf[:n]), "\r\n")
	cl.logf("REQUEST %s selector=%q", remoteAddr, selector)

	switch {
	case selector == "" || selector == "/":
		if pcfg != nil && len(pcfg.URLs) > 1 {
			// Multi-URL mode: serve the generated root gophermap
			gmap := buildRootGopherMap(cfg, pcfg)
			if _, werr := io.WriteString(conn, gmap); werr != nil {
				cl.logf("WRITE ERROR %s: %v", remoteAddr, werr)
			}
		} else {
			serveDirectory(conn, cfg, cfg.BaseURL, remoteAddr, cl)
		}

	case strings.HasPrefix(selector, "/proxy?url="):
		encoded := strings.TrimPrefix(selector, "/proxy?url=")
		targetURL, decErr := url.QueryUnescape(encoded)
		if decErr != nil {
			writeError(conn, "Bad URL encoding")
			cl.logf("ERROR %s bad URL encoding", remoteAddr)
			return
		}
		serveDirectory(conn, cfg, targetURL, remoteAddr, cl)

	case strings.HasPrefix(selector, "/download?url="):
		encoded := strings.TrimPrefix(selector, "/download?url=")
		targetURL, decErr := url.QueryUnescape(encoded)
		if decErr != nil {
			writeError(conn, "Bad URL encoding")
			cl.logf("ERROR %s bad URL encoding", remoteAddr)
			return
		}
		serveFile(conn, targetURL, remoteAddr, cl)

	default:
		writeError(conn, "Unknown selector: "+selector)
		cl.logf("UNKNOWN SELECTOR %s %q", remoteAddr, selector)
	}
}

// serveDirectory fetches and converts an Apache index page to a Gopher menu.
func serveDirectory(conn net.Conn, cfg *Config, httpURL string, remoteAddr string, cl *connLogger) {
	httpURL = ensureScheme(httpURL)
	title, entries, err := parseApacheIndex(httpURL)
	if err != nil {
		cl.logf("ERROR %s parseApacheIndex(%s): %v", remoteAddr, httpURL, err)
		writeError(conn, "Failed to fetch directory: "+err.Error())
		return
	}
	if title == "" {
		title = httpURL
	}
	gopherMap := buildGopherMap(cfg, title, entries, httpURL)
	if _, writeErr := io.WriteString(conn, gopherMap); writeErr != nil {
		cl.logf("WRITE ERROR %s: %v", remoteAddr, writeErr)
		return
	}
	cl.logf("SERVED directory %s -> %s (%d entries)", remoteAddr, httpURL, len(entries))
}

// serveFile proxies a file download from HTTP to the Gopher client.
func serveFile(conn net.Conn, fileURL string, remoteAddr string, cl *connLogger) {
	resp, err := http.Get(fileURL)
	if err != nil {
		cl.logf("ERROR %s file fetch: %v", remoteAddr, err)
		writeError(conn, "Failed to fetch file: "+err.Error())
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		writeError(conn, fmt.Sprintf("HTTP %d fetching file", resp.StatusCode))
		cl.logf("ERROR %s HTTP %d fetching %s", remoteAddr, resp.StatusCode, fileURL)
		return
	}

	written, copyErr := io.Copy(conn, resp.Body)
	if copyErr != nil {
		cl.logf("COPY ERROR %s after %d bytes: %v", remoteAddr, written, copyErr)
		return
	}
	cl.logf("SERVED file %s -> %s (%d bytes)", remoteAddr, fileURL, written)
}

// writeError sends a Gopher error item to the client.
func writeError(conn net.Conn, msg string) {
	fmt.Fprintf(conn, "3%s\terror.host\t1\r\n.\r\n", msg)
}

func main() {
	host := flag.String("host", "localhost", "Hostname advertised in gopher map entries")
	port := flag.Int("port", 7070, "TCP port to listen on")
	baseURL := flag.String("url", "https://bitsavers.org/pdf/", "HTTP URL to proxy (used when no config file is present)")
	logFile := flag.String("log", "", "Path to connection log file (default: log to stderr only)")
	flag.Parse()

	cl, err := newConnLogger(*logFile)
	if err != nil {
		log.Fatalf("Cannot open log file: %v", err)
	}

	// Attempt to load config file
	var pcfg *proxyConfig
	pcfg, err = loadConfigFile(configFile)
	if err != nil {
		if os.IsNotExist(err) {
			cl.logf("INFO no config file (%s) found — running in single-URL mode", configFile)
			writeExampleConfig()
		} else {
			cl.logf("WARNING loading %s: %v — falling back to single-URL mode", configFile, err)
		}
		pcfg = nil
	} else {
		cl.logf("INFO loaded %s: %d URL(s) configured", configFile, len(pcfg.URLs))
		if len(pcfg.URLs) == 1 {
			*baseURL = pcfg.URLs[0].URL
		}
	}

	cfg := &Config{
		Host:    *host,
		Port:    *port,
		BaseURL: ensureScheme(*baseURL),
	}

	addr := fmt.Sprintf(":%d", *port)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatalf("Cannot listen on %s: %v", addr, err)
	}

	cl.logf("INFO Gopher HTTP proxy ready")
	cl.logf("INFO Listen : %s", addr)
	cl.logf("INFO Host   : %s", *host)
	if pcfg != nil && len(pcfg.URLs) > 1 {
		for i, pu := range pcfg.URLs {
			cl.logf("INFO Source[%d]: %s -> %s", i+1, pu.Label, pu.URL)
		}
	} else {
		cl.logf("INFO Source : %s", cfg.BaseURL)
	}
	if *logFile != "" {
		cl.logf("INFO Log file: %s", *logFile)
	}
	cl.logf("INFO Connect with: gopher://%s:%d", *host, *port)

	for {
		conn, acceptErr := ln.Accept()
		if acceptErr != nil {
			cl.logf("ACCEPT ERROR: %v", acceptErr)
			continue
		}
		go handleGopherConn(conn, cfg, pcfg, cl)
	}
}
