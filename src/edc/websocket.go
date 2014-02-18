package edc

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"github.com/fitstar/falcore"
	"github.com/gorilla/websocket"
	"net/http"
	"os"
	"path"
	"sync/atomic"
	"time"
)

type WebsocketWorker struct {
	domain            string
	assetBase         string
	bytesSent         uint64
	messagesSent      uint64
	activeClientCount int64
	brokerAddChan     chan *ActiveClient
	brokerRemChan     chan *ActiveClient
	brokerBCast       chan *ServerPush
	routes            map[string]WebsocketRequestHandler
	NewConnection     func(*ActiveClient)
}

type Request struct {
	Method string                 `json:"method"`
	Data   map[string]interface{} `json:"data"`
}

type ServerPush struct {
	Method string                 `json:"method"`
	Data   map[string]interface{} `json:"data"`
}

type ActiveClient struct {
	ReqID    string
	Session  string
	PushChan chan *ServerPush
	Context  map[string]interface{} // pointer to falcore.Request
	// origReq *falcore.Request // want to avoid these but they might be used for for logging
	// ws *websocket.Conn
}

type WebsocketRequestHandler func(*Request, *ActiveClient)

func NewWebsocketWorker(domain, assetBase string) *WebsocketWorker {
	ww := &WebsocketWorker{
		domain:        domain,
		assetBase:     assetBase,
		brokerAddChan: make(chan *ActiveClient), // blocking
		brokerRemChan: make(chan *ActiveClient, 10),
		brokerBCast:   make(chan *ServerPush, 100),
		routes:        make(map[string]WebsocketRequestHandler),
	}
	go ww.broker()
	return ww
}

func (ww *WebsocketWorker) AddRoute(method string, wsrh WebsocketRequestHandler) {
	// TODO sync?
	ww.routes[method] = wsrh
}

func (ww *WebsocketWorker) broker() {
	wsClients := make(map[string]*ActiveClient)
	for {
		// TODO fix for multiple clients from the same session
		select {
		case add := <-ww.brokerAddChan:
			wsClients[add.Session] = add
			atomic.AddInt64(&ww.activeClientCount, 1)
		case rem := <-ww.brokerRemChan:
			delete(wsClients, rem.Session)
			atomic.AddInt64(&ww.activeClientCount, -1)
		case broadcast := <-ww.brokerBCast:
			for _, reg := range wsClients {
				select {
				case reg.PushChan <- broadcast:
				default:
				}
			}
		}
	}
}

func (ww *WebsocketWorker) GetActiveClientCount() int64 {
	return atomic.LoadInt64(&ww.activeClientCount)
}

func (ww *WebsocketWorker) GetBytesSent() uint64 {
	return atomic.LoadUint64(&ww.bytesSent)
}

func (ww *WebsocketWorker) GetMessagesSent() uint64 {
	return atomic.LoadUint64(&ww.messagesSent)
}

func (ww *WebsocketWorker) SendBroadcast(sp *ServerPush) {
	ww.brokerBCast <- sp
}

func (ww *WebsocketWorker) WebsocketUpgrade(req *falcore.Request, responseHeader http.Header) *http.Response {
	// Associate cookie with this ws
	if sCookie, err := req.HttpRequest.Cookie("_edc_sesison"); err == nil {
		// check domain? nah
		req.Context["session"] = sCookie.Value
	} else {
		// Set a new cookie
		sc := ww.sessionCookie("")
		req.Context["session"] = sc.Value
		responseHeader.Set("Set-Cookie", sc.String())
	}

	return nil
}

func (ww *WebsocketWorker) WebsocketHandler(req *falcore.Request, ws *websocket.Conn) {
	// Register
	sid, _ := req.Context["session"].(string)
	PushChan := make(chan *ServerPush, 5)
	ac := &ActiveClient{
		ReqID:    req.ID,
		Session:  sid,
		PushChan: PushChan,
		Context:  req.Context,
	}
	ww.brokerAddChan <- ac
	defer func() { ww.brokerRemChan <- ac }()
	go ww.websocketReader(ws, ac)
	if ww.NewConnection != nil {
		go ww.NewConnection(ac)
	}
	for sp := range PushChan {
		pkt, err := json.Marshal(sp)
		if err != nil {
			falcore.Error("%v Error encoding json", req.ID)
		} else {
			err := ws.WriteMessage(websocket.TextMessage, pkt)
			if err != nil {
				falcore.Error("%v send err: %v\n", req.ID, err)
				return
			}
			atomic.AddUint64(&ww.messagesSent, 1)
			atomic.AddUint64(&ww.bytesSent, uint64(len(pkt)))
		}
	}
	falcore.Info("%v Writer closed", req.ID)
}

func (ww *WebsocketWorker) websocketReader(ws *websocket.Conn, ac *ActiveClient) {
	defer close(ac.PushChan)
	for {
		_, data, err := ws.ReadMessage()
		if err != nil {
			falcore.Error("%v read err: %v\n", ac.ReqID, err)
			return
		}
		req := &Request{}
		err = json.Unmarshal(data, req)
		if err != nil {
			falcore.Error("%v Malformed request %v", ac.ReqID, err)
		}
		if wsrh, ok := ww.routes[req.Method]; ok {
			go wsrh(req, ac)
		} else {
			falcore.Warn("%v No route for method: %v", ac.ReqID, req.Method)
		}
	}
}

func (ww *WebsocketWorker) FilterRequest(req *falcore.Request) *http.Response {
	uPath := req.HttpRequest.URL.Path
	falcore.Debug("Path: %v", uPath)
	if uPath != "/" && uPath != path.Join(ww.assetBase, "start.html") {
		return nil
	}

	sid := ""
	if sCookie, err := req.HttpRequest.Cookie("_edc_sesison"); err == nil {
		sid = sCookie.Value
	}
	hdrs := make(http.Header)
	hdrs.Set("Set-Cookie", ww.sessionCookie(sid).String())

	hdrs.Set("Content-Type", "text/html")
	if file, err := os.Open(path.Join(ww.assetBase, "start.html")); err == nil {
		if stat, err := file.Stat(); err == nil && stat.Mode()&os.ModeType == 0 {
			res := &http.Response{
				Request:       req.HttpRequest,
				StatusCode:    200,
				Proto:         "HTTP/1.1",
				ProtoMajor:    1,
				ProtoMinor:    1,
				Body:          file,
				Header:        hdrs,
				ContentLength: stat.Size(),
			}
			return res
		} else {
			falcore.Error("Error stat on start.html: %v", err)
		}
	} else {
		falcore.Error("Error opening start.html: %v", err)
	}

	return falcore.StringResponse(req.HttpRequest, 404, nil, "Uh oh, can't find the start page")
}

// session management
func (ww *WebsocketWorker) sessionCookie(sid string) *http.Cookie {
	if sid == "" {
		sid = ww.generateSessionToken()
	}
	c := &http.Cookie{
		Name:    "_edc_sesison",
		Value:   sid,
		Path:    "/",
		Domain:  ww.domain,
		Expires: time.Now().Add(time.Hour * 24 * 365), // 1 year
	}
	return c
}

func (ww *WebsocketWorker) generateSessionToken() string {
	b := make([]byte, 32)
	_, err := rand.Read(b)
	for err != nil {
		time.Sleep(time.Second) // hopefully a seond is long enough to get more random
		_, err = rand.Read(b)
	}
	return base64.URLEncoding.EncodeToString(b)
}
