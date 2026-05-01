# gopher-http-proxy

A Gopher server written in Go that acts as a live proxy for Apache web server
directory listings. It fetches an HTTP directory index, converts it into a
[RFC 1436](https://www.rfc-editor.org/rfc/rfc1436) Gopher menu, and serves it
to any Gopher client — letting you browse and download files from an ordinary
HTTP file server using Gopher protocol.

---

## How it works

```
Gopher client  ──selector──►  gopher-http-proxy  ──HTTP GET──►  Apache web server
               ◄──gophermap──                     ◄──HTML/file──
```

1. A Gopher client connects and sends a **selector string**.
2. The proxy fetches the corresponding HTTP URL from the target Apache server.
3. The HTML directory listing is parsed and converted into a **Gopher map**
   (a menu of typed items).
4. Subdirectory links become navigable Gopher menu items; files become
   directly downloadable items with the appropriate Gopher type.

If a `gopher-proxy.conf` config file is present with multiple URLs, the proxy
generates a custom **top-level root gophermap** — complete with an ASCII art
header — that lets users navigate to each configured source.

The proxy uses **no external dependencies** — only the Go standard library.
HTML parsing is handled by a built-in tokenizer that targets the well-structured
output that Apache's `mod_autoindex` produces.

---

## Requirements

- Go 1.22 or later
- Network access to the target HTTP server

---

## Building

```bash
git clone https://github.com/you/gopher-http-proxy
cd gopher-http-proxy
go build -o gopher-http-proxy .
```

---

## Usage

```
./gopher-http-proxy [-url <host/path>] [-host <gopher-host>] [-port <port>] [-log <logfile>]
```

### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `-url` | `https://bitsavers.org/pdf/` | The HTTP URL to proxy. The scheme (`https://`) is added automatically if omitted. Ignored when a config file with multiple URLs is present. |
| `-host` | `localhost` | Hostname advertised inside generated Gopher map entries. Should match the hostname your Gopher clients will connect to. |
| `-port` | `7070` | TCP port to listen on. Port 70 is the standard Gopher port and requires root on Linux; `7070` is the default to allow running without elevated privileges. |
| `-log` | _(stderr only)_ | Path to a log file. Connection events are written to **both** stderr and this file simultaneously. If omitted, output goes to stderr only. |

### Examples

Run with all defaults (proxies `https://bitsavers.org/pdf/` on port 7070):

```bash
./gopher-http-proxy
```

Override host and port for a public-facing server on the standard Gopher port:

```bash
sudo ./gopher-http-proxy \
  -url  bitsavers.org/pdf \
  -host gopher.example.com \
  -port 70
```

Proxy a different site and write a connection log:

```bash
./gopher-http-proxy \
  -url  ftp.gnu.org/gnu \
  -host localhost \
  -port 7070 \
  -log  /var/log/gopher-proxy.log
```

Then connect with any Gopher client:

```bash
# lynx
lynx gopher://localhost:7070

# Bombadillo
bombadillo gopher://localhost:7070

# curl (Gopher support built-in)
curl gopher://localhost:7070/
```

---

## Config file

The proxy looks for `gopher-proxy.conf` in the current working directory at
startup. This file is **optional** — without it, the proxy falls back to
single-URL mode using the `-url` flag.

When no config file is found, the proxy writes a fully commented example to
`gopher-proxy.conf.example` so you have a template to start from.

### Format

```ini
# Lines starting with '#' and blank lines are ignored.

[header]
Lines here are displayed as Gopher type 'i' (info) items
at the top of the root gophermap — ASCII art is perfect here.

[urls]
Group Title
_______________________________
Display Label      | https://host/path/
Another Label      | ftp.gnu.org/gnu

Another Group
_______________________________
https://bare.url/
```

Two sections are supported:

**`[header]`** _(optional)_  
Each line becomes a `type-i` informational item in the root gophermap, rendered
before the separator. Use this for ASCII art, a welcome message, or a banner.

**`[urls]`** _(required when using a config file)_  
One URL per line, in the format `Label | URL`. If no `|` separator is present,
the raw URL is used as both the label and the target. The `https://` scheme is
added automatically if omitted.

URLs can optionally be organised into named **groups**. A group starts with a
title line followed immediately by the fixed separator string
`_______________________________`. All URL lines that follow belong to that
group, until the next group title or the end of the file. Separate groups with
a blank line for readability. URLs listed before any group title are collected
into an anonymous group and rendered without a heading.

### Example config

```ini
# gopher-proxy.conf

[header]
  __ _  ___  _ __  | _ __ _ __ _____  ___   _
 / _` |/ _ \| '_ \ | '_ \| '__/ _ \ \/ / | | |
| (_| | (_) | |_) || |_) | | | (_) >  <| |_| |
 \__, |\___/| .__/ | .__/|_|  \___/_/\_\\__, |
  |___/     |_|    |_|                  |___/
Welcome to the Gopher HTTP proxy.

[urls]
Bitsavers.org
_______________________________
Bitsavers PDF archive    | https://bitsavers.org/pdf/
Bitsavers bits archive   | https://bitsavers.org/bits/

GNU
_______________________________
GNU FTP archive          | https://ftp.gnu.org/gnu/
```

### Behaviour depending on config

| Situation | Root gophermap |
|-----------|----------------|
| No config file | Single-URL mode using `-url` flag |
| Config file with 1 URL | Single-URL mode, URL taken from config |
| Config file with 2+ URLs, no groups | Top-level menu with header, separator, and a `type-1` selector per URL |
| Config file with grouped URLs | Top-level menu with header, then each group rendered as a titled sub-section |

### Root gophermap layout (multi-URL mode)

```
i <header line 1>
i <header line 2>
i ...
i _______________________________
i
i <Group A title>
i _______________________________
1 Label A1    /proxy?url=<encoded>   host   port
1 Label A2    /proxy?url=<encoded>   host   port
i
i <Group B title>
i _______________________________
1 Label B1    /proxy?url=<encoded>   host   port
.
```

The header section is separated from the URL groups by the fixed string
`_______________________________`. Each group then begins with its title and
another separator, followed by its `type-1` directory selectors. Groups are
separated by a blank `type-i` line. Anonymous groups (URLs listed before any
titled group) are rendered without a title or separator of their own.

---

## Connection logging

All connection events are written with a timestamp in the format
`[2006-01-02 15:04:05]`.  When a `-log` path is provided, events go to **both**
stderr and the file, so you always see live output in the terminal while
keeping a persistent record on disk.

Events logged per connection:

| Event | Logged when |
|-------|-------------|
| `CONNECT` | A client opens a TCP connection |
| `REQUEST` | The selector string has been read |
| `SERVED directory` | A gophermap was sent successfully (includes entry count) |
| `SERVED file` | A file was proxied successfully (includes byte count) |
| `ERROR` | An upstream HTTP error or URL decode failure occurred |
| `COPY ERROR` | The connection dropped mid-transfer |
| `WRITE ERROR` | Writing to the Gopher client failed |
| `UNKNOWN SELECTOR` | The client sent an unrecognised selector |

Example log output:

```
[2024-08-15 14:32:01] INFO Gopher HTTP proxy ready
[2024-08-15 14:32:01] INFO Listen : :7070
[2024-08-15 14:32:01] INFO Source[1]: Bitsavers PDF archive -> https://bitsavers.org/pdf/
[2024-08-15 14:32:01] INFO Source[2]: GNU FTP archive -> https://ftp.gnu.org/gnu/
[2024-08-15 14:32:01] INFO Log file: /var/log/gopher-proxy.log
[2024-08-15 14:32:05] CONNECT 192.168.1.42:51234
[2024-08-15 14:32:05] REQUEST 192.168.1.42:51234 selector=""
[2024-08-15 14:32:05] SERVED directory 192.168.1.42:51234 -> https://bitsavers.org/pdf/ (312 entries)
```

---

## Selector protocol

The proxy uses a simple internal selector scheme:

| Selector | Meaning |
|----------|---------|
| (empty) or `/` | Render the root `-url` as a Gopher menu, or the multi-URL root gophermap if a config file is present |
| `/proxy?url=<encoded>` | Fetch a subdirectory and render it as a Gopher menu |
| `/download?url=<encoded>` | Stream a file directly from HTTP to the Gopher client |

Clients do not need to construct these selectors manually — they are
embedded in every generated Gopher map and followed automatically by
the client when the user selects an item.

---

## Gopher item types

Files are assigned RFC 1436 item types based on their extension:

| Type code | Meaning | Extensions |
|-----------|---------|------------|
| `1` | Directory / submenu | Paths ending in `/` |
| `0` | Plain text file | `.txt` `.md` `.log` `.cfg` `.conf` `.ini` `.csv` `.nfo` |
| `g` | GIF image | `.gif` |
| `I` | Image | `.jpg` `.jpeg` `.png` `.bmp` `.tif` `.tiff` |
| `d` | PDF document | `.pdf` |
| `9` | Binary file | Everything else (`.zip`, `.bin`, …) |

---

## Internals

### `parseApacheIndex`
Fetches the HTTP URL and walks the HTML token stream looking for:
- The `<title>` element — used as the menu heading.
- `<a href="...">` links — each becomes a `DirEntry`.

Apache's own navigation links are filtered out: the parent directory (`../`),
column-sort query strings (`?C=N&O=D`), and `mailto:` links are all skipped.

### `buildGopherMap`
Renders a `DirEntry` slice into a Gopher map string. Each line follows the
RFC 1436 format:

```
<type><display-name>\t<selector>\t<host>\t<port>\r\n
```

Informational header lines use type `i` with placeholder host/port values.

### `buildRootGopherMap`
Renders the top-level gophermap from the config file. Header lines from the
`[header]` section become `type-i` info items; after the main separator, each
`URLGroup` is rendered as a titled sub-section (group title + separator as
`type-i` info lines, followed by `type-1` directory selectors for each URL).
Groups without a title skip the heading and separator. A blank `type-i` line
is inserted between groups.

### `handleGopherConn`
Reads the selector from the TCP connection, logs the connection and request,
routes it to `serveDirectory` or `serveFile`, and closes the connection when
done. Each connection is handled in its own goroutine.

### `loadConfigFile`
Parses `gopher-proxy.conf`, extracting `[header]` lines and the raw URL lines
from `[urls]`. The URL lines are passed to `parseURLGroups` to produce the
final `[]URLGroup` structure. Returns `os.ErrNotExist` if the file is absent
so the caller can fall back gracefully to single-URL mode.

### `parseURLGroups`
Converts the raw lines from the `[urls]` section into `URLGroup` values using
a single-pass look-ahead: if the next non-blank line after the current line is
the separator string `_______________________________`, the current line is
treated as a group title and a new group is opened. Otherwise the line is
parsed as a `Label | URL` entry and appended to the current group. This means
the old flat (ungrouped) format is fully backwards-compatible — all URLs land
in a single anonymous group.

### HTML tokenizer
A minimal hand-written tokenizer (`tokenizer` / `token`) processes the raw
HTML byte slice without any external package. It handles quoted and unquoted
attribute values, comments, and DOCTYPE declarations, which is sufficient for
the regular, machine-generated HTML that Apache `mod_autoindex` produces.

---

## Known limitations

- **Apache only.** The HTML parser targets the specific structure that Apache's
  `mod_autoindex` generates. Other directory listing implementations (nginx,
  Caddy, etc.) may not parse correctly.
- **Static content only.** The proxy does not support authentication, cookies,
  or JavaScript-rendered pages.
- **Some servers block automated clients.** If the target server returns
  HTTP 403, it may be rejecting non-browser `User-Agent` strings. You can
  work around this by replacing the bare `http.Get` calls in `main.go` with
  a custom `http.Request` that sets a `User-Agent` header.
- **No caching.** Every Gopher request triggers a fresh HTTP fetch from the
  upstream server.
- **Port 70 requires elevated privileges** on Linux/macOS. Use `sudo`, or
  configure a firewall rule to redirect port 70 to a higher port.

---

## License

MIT
