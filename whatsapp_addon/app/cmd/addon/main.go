package main

import (
	"bytes"
	"bufio"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"io"
	"log"
	"mime"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/mdp/qrterminal/v3"
	"github.com/hajimehoshi/go-mp3"
	"github.com/jfreymuth/oggvorbis"
	rscqr "rsc.io/qr"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"

	waProto "go.mau.fi/whatsmeow/proto/waE2E"
	"google.golang.org/protobuf/proto"

	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	_ "modernc.org/sqlite"
)

const (
	statusOk = "OK"
	statusKo = "KO"
)

func writeKO(w http.ResponseWriter, status int, reason string) {
	w.WriteHeader(status)
	_, _ = w.Write([]byte(statusKo + ": " + reason))
}

type clientWrap struct {
	id     string
	cli    *whatsmeow.Client
	cancel context.CancelFunc

	mu     sync.RWMutex
	ready  bool
	lastQR string // latest QR code string
}

type manager struct {
	mu      sync.RWMutex
	clients map[string]*clientWrap
}

type sendTextReq struct {
	ClientID string `json:"clientId"`
	To       string `json:"to"`
	Body     struct {
		Text string `json:"text"`
	} `json:"body"`
}

type mediaBody struct {
	URL      string `json:"url"`
	Caption  string `json:"caption"`
	FileName string `json:"fileName"`
	MimeType string `json:"mimeType"`
	PTT      bool   `json:"ptt"`
}

type sendMediaReq struct {
	ClientID string    `json:"clientId"`
	To       string    `json:"to"`
	Body     mediaBody `json:"body"`
}

// statusWriter lets us log the response status code.
type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

func loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sw := &statusWriter{ResponseWriter: w, status: 200}
		start := time.Now()
		next.ServeHTTP(sw, r)
		log.Printf("HTTP %s %s -> %d (%s) from=%s ua=%q",
			r.Method, r.URL.Path, sw.status, time.Since(start), r.RemoteAddr, r.UserAgent())
	})
}

func main() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	logger := waLog.Stdout("whatsapp-addon", "INFO", true)

	// Whatsmeow store (SQLite, pure-go).
	// Prefer STORE_PATH from the add-on run script, but keep WHATSAPP_DB for compatibility.
	dbPath := "/data/store.db"
	if v := strings.TrimSpace(os.Getenv("STORE_PATH")); v != "" {
		dbPath = v
	} else if v := strings.TrimSpace(os.Getenv("WHATSAPP_DB")); v != "" {
		dbPath = v
	}
	_ = os.MkdirAll(filepath.Dir(dbPath), 0o755)
	log.Printf("using sqlite store at %s", dbPath)

	// Use a single SQLite connection and enable WAL + busy timeout to reduce SQLITE_BUSY
	// during the burst of concurrent writes that happens right after pairing/history sync.
	dbDSN := fmt.Sprintf(
		"file:%s?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(10000)",
		dbPath,
	)
	db, err := sql.Open("sqlite", dbDSN)
	if err != nil {
		log.Fatalf("failed to open sqlite store: %v", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	defer db.Close()

	storeContainer := sqlstore.NewWithDB(db, "sqlite3", logger)
	if err := storeContainer.Upgrade(ctx); err != nil {
		log.Fatalf("failed to init store: %v", err)
	}

	m := &manager{clients: map[string]*clientWrap{}}

	// default client
	defaultID := "default"
	cw, err := m.newClient(ctx, storeContainer, defaultID, logger)
	if err != nil {
		log.Fatalf("failed to init default client: %v", err)
	}
	m.mu.Lock()
	m.clients[defaultID] = cw
	m.mu.Unlock()

	// Best-effort: connect + show QR in logs + notify in HA if needed
	go cw.ensureConnectedAndMaybeQR(ctx)

	mux := http.NewServeMux()

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("WhatsApp addon running"))
	})

	mux.HandleFunc("/sendMessage", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		var req sendTextReq
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			log.Printf("sendMessage: bad json: %v", err)
			writeKO(w, http.StatusBadRequest, "bad json")
			return
		}

		c := m.mustClient(req.ClientID)
		if c == nil {
			log.Printf("sendMessage: unknown clientId=%q", req.ClientID)
			writeKO(w, http.StatusBadRequest, "unknown clientId")
			return
		}

		jid, err := parseJID(req.To)
		if err != nil {
			log.Printf("sendMessage: bad to=%q err=%v", req.To, err)
			writeKO(w, http.StatusBadRequest, "bad to")
			return
		}

		if strings.TrimSpace(req.Body.Text) == "" {
			log.Printf("sendMessage: empty text body")
			writeKO(w, http.StatusBadRequest, "empty text")
			return
		}

		msg := &waProto.Message{Conversation: proto.String(req.Body.Text)}
		_, err = c.cli.SendMessage(context.Background(), jid, msg)
		if err != nil {
			log.Printf("sendMessage failed: clientId=%s to=%s err=%v", req.ClientID, req.To, err)
			writeKO(w, http.StatusInternalServerError, "send failed")
			return
		}

		_, _ = w.Write([]byte(statusOk))
	})

	mux.HandleFunc("/sendImage", func(w http.ResponseWriter, r *http.Request) {
		handleMedia(w, r, m, "image")
	})
	mux.HandleFunc("/sendVideo", func(w http.ResponseWriter, r *http.Request) {
		handleMedia(w, r, m, "video")
	})
	mux.HandleFunc("/sendDocument", func(w http.ResponseWriter, r *http.Request) {
		handleMedia(w, r, m, "document")
	})
	mux.HandleFunc("/sendAudio", func(w http.ResponseWriter, r *http.Request) {
		handleMedia(w, r, m, "audio")
	})

	// Status endpoint (includes lastQR string)
	mux.HandleFunc("/status", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}

		m.mu.RLock()
		ids := make([]string, 0, len(m.clients))
		for id := range m.clients {
			ids = append(ids, id)
		}
		m.mu.RUnlock()
		sort.Strings(ids)

		out := make(map[string]map[string]any, len(ids))
		m.mu.RLock()
		for _, id := range ids {
			cw := m.clients[id]
			if cw == nil || cw.cli == nil {
				out[id] = map[string]any{"connected": false, "paired": false}
				continue
			}

			paired := cw.cli.Store != nil && cw.cli.Store.ID != nil
			jid := ""
			if paired {
				jid = cw.cli.Store.ID.String()
			}

			cw.mu.RLock()
			lastQR := cw.lastQR
			ready := cw.ready
			cw.mu.RUnlock()

			out[id] = map[string]any{
				"connected": cw.cli.IsConnected(),
				"paired":    paired,
				"ready":     ready,
				"jid":       jid,
				"lastQR":    lastQR,
			}
		}
		m.mu.RUnlock()

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(out)
	})

	// QR endpoint (returns lastQR + base64 PNG if available)
	mux.HandleFunc("/qr", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		cw := m.mustClient("") // default
		if cw == nil {
			w.WriteHeader(http.StatusNotFound)
			return
		}

		cw.mu.RLock()
		qrText := cw.lastQR
		cw.mu.RUnlock()

		resp := map[string]any{
			"clientId": "default",
			"qrText":   qrText,
		}
		if strings.TrimSpace(qrText) != "" {
			if b64, err := qrPNGBase64(qrText, 256); err == nil {
				resp["qrPngBase64"] = b64
			} else {
				resp["qrError"] = err.Error()
			}
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	})

	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		_, _ = w.Write([]byte("OK"))
	})

	srv := &http.Server{
		Addr:    ":3000",
		Handler: loggingMiddleware(mux),
	}

	go func() {
		log.Printf("listening on %s (clients=%v)", srv.Addr, m.listClientIDs())
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("http server error: %v", err)
		}
	}()

	// shutdown handling
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh

	_ = srv.Shutdown(context.Background())
	cancel()
}

func (m *manager) listClientIDs() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	ids := make([]string, 0, len(m.clients))
	for id := range m.clients {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func (m *manager) mustClient(id string) *clientWrap {
	if id == "" {
		id = "default"
	}
	m.mu.RLock()
	c := m.clients[id]
	m.mu.RUnlock()
	return c
}

func (m *manager) newClient(ctx context.Context, storeContainer *sqlstore.Container, id string, logger waLog.Logger) (*clientWrap, error) {
	// NEWER whatsmeow requires context here
	deviceStore, err := storeContainer.GetFirstDevice(ctx)
	if err != nil {
		return nil, err
	}

	client := whatsmeow.NewClient(deviceStore, logger)

	cctx, cancel := context.WithCancel(ctx)
	cw := &clientWrap{id: id, cli: client, cancel: cancel}

	client.AddEventHandler(func(evt interface{}) {
		switch evt.(type) {
		case *events.Connected:
			cw.mu.Lock()
			cw.ready = true
			cw.mu.Unlock()
		case *events.Disconnected:
			cw.mu.Lock()
			cw.ready = false
			cw.mu.Unlock()
		case *events.LoggedOut:
			cw.mu.Lock()
			cw.ready = false
			cw.lastQR = ""
			cw.mu.Unlock()
		}
	})

	go func() {
		<-cctx.Done()
		client.Disconnect()
	}()

	return cw, nil
}

func (cw *clientWrap) ensureConnectedAndMaybeQR(ctx context.Context) {
	var lastNotifiedQR string

	for {
		if ctx.Err() != nil {
			return
		}

		if cw.cli.IsConnected() {
			time.Sleep(3 * time.Second)
			continue
		}

		// If not paired, we must get QR channel BEFORE connect
		if cw.cli.Store == nil || cw.cli.Store.ID == nil {
			qrChan, err := cw.cli.GetQRChannel(ctx)
			if err != nil {
				log.Printf("[%s] GetQRChannel failed: %v", cw.id, err)
				time.Sleep(5 * time.Second)
				continue
			}

			if err := cw.cli.Connect(); err != nil {
				log.Printf("[%s] connect failed: %v", cw.id, err)
				time.Sleep(5 * time.Second)
				continue
			}

			for evt := range qrChan {
				if evt.Event != whatsmeow.QRChannelEventCode {
					cw.mu.Lock()
					cw.lastQR = ""
					cw.mu.Unlock()
					log.Printf("[%s] QR channel finished with event=%s", cw.id, evt.Event)
					continue
				}

				qrText := strings.TrimSpace(evt.Code)
				if qrText == "" {
					log.Printf("[%s] QR channel emitted empty code", cw.id)
					continue
				}

				cw.mu.Lock()
				cw.lastQR = qrText
				cw.mu.Unlock()

				log.Printf("[%s] QR updated", cw.id)
				qrterminal.GenerateHalfBlock(qrText, qrterminal.L, os.Stdout)

				// Notify HA (only if changed)
				if qrText != lastNotifiedQR {
					lastNotifiedQR = qrText
					if b64, err := qrPNGBase64(qrText, 256); err == nil {
						msg := "Scan this QR with WhatsApp to pair:\n\n" +
							"![WhatsApp QR](data:image/png;base64," + b64 + ")\n\n" +
							"(If it doesn’t render, open add-on logs to see the ASCII QR.)"
						if err := supervisorNotify("WhatsApp Pairing QR", msg); err != nil {
							log.Printf("[%s] notify failed: %v", cw.id, err)
						} else {
							log.Printf("[%s] QR pushed to persistent_notification", cw.id)
						}
					} else {
						log.Printf("[%s] qr png encode failed: %v", cw.id, err)
					}
				}
			}

			time.Sleep(2 * time.Second)
			continue
		}

		// Paired: just connect
		if err := cw.cli.Connect(); err != nil {
			log.Printf("[%s] connect failed: %v", cw.id, err)
			time.Sleep(5 * time.Second)
			continue
		}

		time.Sleep(2 * time.Second)
	}
}

func handleMedia(w http.ResponseWriter, r *http.Request, m *manager, kind string) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	var req sendMediaReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		log.Printf("send%s: bad json: %v", strings.Title(kind), err)
		writeKO(w, http.StatusBadRequest, "bad json")
		return
	}

	cw := m.mustClient(req.ClientID)
	if cw == nil {
		log.Printf("send%s: unknown clientId=%q", strings.Title(kind), req.ClientID)
		writeKO(w, http.StatusBadRequest, "unknown clientId")
		return
	}

	jid, err := parseJID(req.To)
	if err != nil {
		log.Printf("send%s: bad to=%q err=%v", strings.Title(kind), req.To, err)
		writeKO(w, http.StatusBadRequest, "bad to")
		return
	}

	if strings.TrimSpace(req.Body.URL) == "" {
		log.Printf("send%s: blank url", strings.Title(kind))
		writeKO(w, http.StatusBadRequest, "blank url")
		return
	}

	data, detectedMimeType, err := loadMediaSource(req.Body.URL)
	if err != nil {
		log.Printf("send%s: failed to load source=%q err=%v", strings.Title(kind), req.Body.URL, err)
		writeKO(w, http.StatusBadGateway, "fetch failed")
		return
	}

	mimeType := strings.TrimSpace(req.Body.MimeType)
	if mimeType == "" {
		mimeType = detectedMimeType
	}
	if mimeType == "" {
		mimeType = http.DetectContentType(data)
	}

	if kind == "document" && req.Body.FileName == "" {
		exts, _ := mime.ExtensionsByType(mimeType)
		ext := ""
		if len(exts) > 0 {
			ext = exts[0]
		}
		req.Body.FileName = "file" + ext
	}

	var mediaType whatsmeow.MediaType
	switch kind {
	case "image":
		mediaType = whatsmeow.MediaImage
	case "video":
		mediaType = whatsmeow.MediaVideo
	case "audio":
		mediaType = whatsmeow.MediaAudio
	case "document":
		mediaType = whatsmeow.MediaDocument
	default:
		log.Printf("send%s: unsupported media kind=%q", strings.Title(kind), kind)
		writeKO(w, http.StatusBadRequest, "unsupported media kind")
		return
	}

	up, err := cw.cli.Upload(context.Background(), data, mediaType)
	if err != nil {
		log.Printf("send%s: upload failed err=%v", strings.Title(kind), err)
		writeKO(w, http.StatusInternalServerError, "upload failed")
		return
	}

	imageWidth, imageHeight := getImageDimensions(data)
	audioSeconds := getAudioDurationSeconds(data, mimeType)

	var msg *waProto.Message
	switch kind {
	case "image":
		msg = &waProto.Message{
			ImageMessage: &waProto.ImageMessage{
				URL:           proto.String(up.URL),
				Mimetype:      proto.String(mimeType),
				Caption:       proto.String(req.Body.Caption),
				FileLength:    proto.Uint64(up.FileLength),
				FileSHA256:    up.FileSHA256,
				FileEncSHA256: up.FileEncSHA256,
				MediaKey:      up.MediaKey,
				DirectPath:    proto.String(up.DirectPath),
				Width:         imageWidth,
				Height:        imageHeight,
			},
		}
	case "video":
		msg = &waProto.Message{
			VideoMessage: &waProto.VideoMessage{
				URL:           proto.String(up.URL),
				Mimetype:      proto.String(mimeType),
				Caption:       proto.String(req.Body.Caption),
				FileLength:    proto.Uint64(up.FileLength),
				FileSHA256:    up.FileSHA256,
				FileEncSHA256: up.FileEncSHA256,
				MediaKey:      up.MediaKey,
				DirectPath:    proto.String(up.DirectPath),
			},
		}
	case "audio":
		msg = &waProto.Message{
			AudioMessage: &waProto.AudioMessage{
				URL:           proto.String(up.URL),
				Mimetype:      proto.String(mimeType),
				FileLength:    proto.Uint64(up.FileLength),
				Seconds:       audioSeconds,
				FileSHA256:    up.FileSHA256,
				FileEncSHA256: up.FileEncSHA256,
				MediaKey:      up.MediaKey,
				DirectPath:    proto.String(up.DirectPath),
				PTT:           proto.Bool(req.Body.PTT),
			},
		}
	case "document":
		fn := req.Body.FileName
		if fn == "" {
			fn = "file"
		}
		msg = &waProto.Message{
			DocumentMessage: &waProto.DocumentMessage{
				URL:           proto.String(up.URL),
				Mimetype:      proto.String(mimeType),
				Title:         proto.String(fn),
				FileName:      proto.String(fn),
				Caption:       proto.String(req.Body.Caption),
				FileLength:    proto.Uint64(up.FileLength),
				FileSHA256:    up.FileSHA256,
				FileEncSHA256: up.FileEncSHA256,
				MediaKey:      up.MediaKey,
				DirectPath:    proto.String(up.DirectPath),
			},
		}
	default:
		msg = &waProto.Message{Conversation: proto.String(req.Body.Caption)}
	}

	_, err = cw.cli.SendMessage(context.Background(), jid, msg)
	if err != nil {
		log.Printf("send%s failed: clientId=%s to=%s err=%v", strings.Title(kind), req.ClientID, req.To, err)
		writeKO(w, http.StatusInternalServerError, "send failed")
		return
	}

	_, _ = w.Write([]byte(statusOk))
}

func loadMediaSource(source string) ([]byte, string, error) {
	source = strings.TrimSpace(source)
	if source == "" {
		return nil, "", fmt.Errorf("empty media source")
	}

	u, err := url.Parse(source)
	if err == nil && (u.Scheme == "http" || u.Scheme == "https") {
		resp, err := http.Get(source) // #nosec G107
		if err != nil {
			return nil, "", err
		}
		defer resp.Body.Close()

		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return nil, "", fmt.Errorf("upstream returned %d", resp.StatusCode)
		}

		data, err := io.ReadAll(resp.Body)
		if err != nil {
			return nil, "", err
		}
		return data, resp.Header.Get("Content-Type"), nil
	}
	if err == nil && u.Scheme != "" {
		return nil, "", fmt.Errorf("unsupported media source scheme %q", u.Scheme)
	}

	filePath, err := resolveLocalMediaPath(source)
	if err != nil {
		return nil, "", err
	}

	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, "", err
	}

	return data, mime.TypeByExtension(strings.ToLower(filepath.Ext(filePath))), nil
}

func resolveLocalMediaPath(source string) (string, error) {
	switch {
	case strings.HasPrefix(source, "/local/"):
		return safeJoinLocalPath("/config/www", strings.TrimPrefix(source, "/local/"))
	case source == "/config/www" || strings.HasPrefix(source, "/config/www/"):
		return safeAbsoluteLocalPath(source, "/config/www")
	case source == "/media" || strings.HasPrefix(source, "/media/"):
		return safeAbsoluteLocalPath(source, "/media")
	default:
		return "", fmt.Errorf("unsupported local media path %q", source)
	}
}

func safeJoinLocalPath(root, relative string) (string, error) {
	return safeAbsoluteLocalPath(filepath.Join(root, relative), root)
}

func safeAbsoluteLocalPath(pathToCheck, root string) (string, error) {
	cleanRoot := filepath.Clean(root)
	cleanPath := filepath.Clean(pathToCheck)
	if cleanPath != cleanRoot && !strings.HasPrefix(cleanPath, cleanRoot+string(os.PathSeparator)) {
		return "", fmt.Errorf("path %q escapes allowed root %q", pathToCheck, root)
	}
	return cleanPath, nil
}

func getImageDimensions(data []byte) (*uint32, *uint32) {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || cfg.Width <= 0 || cfg.Height <= 0 {
		return nil, nil
	}

	width := uint32(cfg.Width)
	height := uint32(cfg.Height)
	return &width, &height
}

func getAudioDurationSeconds(data []byte, mimeType string) *uint32 {
	mimeType = strings.ToLower(strings.TrimSpace(mimeType))

	switch {
	case mimeType == "audio/mpeg", mimeType == "audio/mp3":
		decoder, err := mp3.NewDecoder(bytes.NewReader(data))
		if err != nil {
			return nil
		}
		length := decoder.Length()
		sampleRate := decoder.SampleRate()
		if length <= 0 || sampleRate <= 0 {
			return nil
		}
		// go-mp3 decodes to 16-bit stereo PCM: 4 bytes per sample frame.
		seconds := uint32(length / 4 / int64(sampleRate))
		if seconds == 0 {
			seconds = 1
		}
		return &seconds
	case mimeType == "audio/ogg", mimeType == "audio/vorbis":
		reader, err := oggvorbis.NewReader(bytes.NewReader(data))
		if err != nil {
			return nil
		}
		length := reader.Length()
		sampleRate := reader.SampleRate()
		if length <= 0 || sampleRate <= 0 {
			return nil
		}
		seconds := uint32(length / int64(sampleRate))
		if seconds == 0 {
			seconds = 1
		}
		return &seconds
	default:
		return nil
	}
}

func parseJID(to string) (types.JID, error) {
	to = strings.TrimSpace(to)
	if to == "" {
		return types.JID{}, fmt.Errorf("empty 'to'")
	}

	if strings.Contains(to, "@") {
		jid, err := types.ParseJID(to)
		if err != nil {
			return types.JID{}, err
		}
		return jid, nil
	}

	if strings.Contains(to, "-") {
		return types.ParseJID(to + "@g.us")
	}
	return types.ParseJID(to + "@s.whatsapp.net")
}

func qrPNGBase64(qrText string, _ int) (string, error) {
	if strings.TrimSpace(qrText) == "" {
		return "", fmt.Errorf("empty qr text")
	}

	code, err := rscqr.Encode(qrText, rscqr.M)
	if err != nil {
		return "", err
	}

	pngBytes := code.PNG()

	return base64.StdEncoding.EncodeToString(pngBytes), nil
}

func supervisorNotify(title, message string) error {
	token := os.Getenv("SUPERVISOR_TOKEN")
	if token == "" {
		token = os.Getenv("HASSIO_TOKEN")
	}
	if token == "" {
		return fmt.Errorf("missing SUPERVISOR_TOKEN/HASSIO_TOKEN")
	}

	payload := map[string]any{
		"title":   title,
		"message": message,
	}

	b, _ := json.Marshal(payload)

	req, err := http.NewRequest(
		http.MethodPost,
		"http://supervisor/core/api/services/persistent_notification/create",
		bytes.NewReader(b),
	)
	if err != nil {
		return err
	}

	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	cli := &http.Client{Timeout: 10 * time.Second}
	resp, err := cli.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("notify failed: %s body=%s", resp.Status, string(body))
	}

	return nil
}

// Small “anchors” to prevent accidental unused imports during iterative edits
var (
	_ = runtime.GOOS
	_ = sql.ErrNoRows
	_ = errors.New
	_ = bufio.NewReader
)
