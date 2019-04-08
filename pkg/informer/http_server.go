package informer

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"

	utilnet "k8s.io/apimachinery/pkg/util/net"
	"k8s.io/apimachinery/pkg/util/proxy"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/transport"
)

// HTTPServerLocations type
type HTTPServerLocations struct {
	Health                        string
	DefaultIndex, IndexPrefix     string
	APIProxyPrefix, APIProxyAllow string
}

func writeJSON(res http.ResponseWriter, statusCode int, data interface{}) error {
	body, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("failed to marshal json: %v", err)
	}
	res.Header().Set("Content-Type", "application/json")
	res.WriteHeader(statusCode)
	res.Write(body)
	return nil
}

func handleHealthRequest(loc string, res http.ResponseWriter, req *http.Request, informer Informer) error {
	if !informer.Active() {
		return writeJSON(res, http.StatusServiceUnavailable, map[string]string{"status": "DOWN"})
	}
	return writeJSON(res, http.StatusOK, map[string]string{"status": "UP"})
}

func intParam(req *http.Request, name string, defaultValue int) int {
	if val := req.FormValue(name); val != "" {
		if i, err := strconv.Atoi(val); err == nil {
			return i
		}
	}
	return defaultValue
}

func listStrings(strs []string) []interface{} {
	list := make([]interface{}, len(strs))
	for i, str := range strs {
		list[i] = str
	}
	return list
}

func listIndexerNames(indexers cache.Indexers) []interface{} {
	list, i := make([]interface{}, len(indexers)), 0
	for k := range indexers {
		list[i] = k
		i++
	}
	return list
}

func writeJSONList(res http.ResponseWriter, req *http.Request, list []interface{}, field string) error {
	offset, limit, total := intParam(req, "offset", 0), intParam(req, "limit", 200), len(list)
	if total == 0 || offset < 0 || offset >= total || limit <= 0 {
		list = []interface{}{}
	} else {
		list = list[offset:]
		if limit < len(list) {
			list = list[:limit]
		}
	}
	return writeJSON(res, http.StatusOK, map[string]interface{}{"total": total, field: list})
}

func handleDefaultIndexRequest(loc string, res http.ResponseWriter, req *http.Request, informer Informer) error {
	key, watch := req.FormValue("key"), intParam(req, "watch", 0)
	indexer, ok := informer.GetIndexer(watch)
	if !ok {
		return fmt.Errorf("watch %v not exists", watch)
	}
	if _, keys := req.Form["keys"]; keys {
		return writeJSONList(res, req, listStrings(indexer.ListKeys()), "keys")
	}
	if _, list := req.Form["list"]; list {
		return writeJSONList(res, req, indexer.List(), "items")
	}
	obj, exists, err := indexer.GetByKey(key)
	if err != nil {
		return fmt.Errorf("failed to get by key: %v", err)
	}
	if exists {
		return writeJSONList(res, req, []interface{}{obj}, "items")
	}
	return writeJSONList(res, req, []interface{}{}, "items")
}

func handleIndexRequest(loc string, res http.ResponseWriter, req *http.Request, informer Informer) error {
	location, key, watch := req.URL.Path, req.FormValue("key"), intParam(req, "watch", 0)
	if !strings.HasPrefix(location, loc) {
		return fmt.Errorf("illegal location %s", location)
	}
	indexer, ok := informer.GetIndexer(watch)
	if !ok {
		return fmt.Errorf("watch %v not exists", watch)
	}
	indexName := strings.TrimPrefix(location, loc)
	if indexName == "" {
		return writeJSONList(res, req, listIndexerNames(indexer.GetIndexers()), "indexes")
	}
	if _, keys := req.Form["keys"]; keys {
		return writeJSONList(res, req, listStrings(indexer.ListIndexFuncValues(indexName)), "keys")
	}
	list, err := indexer.ByIndex(indexName, key)
	if err != nil {
		return fmt.Errorf("failed to get by index: %v", err)
	}
	return writeJSONList(res, req, list, "items")
}

func handleProxyRequest() {

}

func (i *informer) EnableIndexServer(serverAddr string) *http.ServeMux {
	return i.EnableHTTPServerWithLocations(serverAddr, HTTPServerLocations{
		Health:       "/health",
		DefaultIndex: "/index",
		IndexPrefix:  "/index/",
	})
}

func (i *informer) EnableHTTPServerWithLocations(serverAddr string, locations HTTPServerLocations) *http.ServeMux {
	serverMux, informerHandler := http.NewServeMux(), func(location string, handler func(string, http.ResponseWriter, *http.Request, Informer) error) func(http.ResponseWriter, *http.Request) {
		return func(res http.ResponseWriter, req *http.Request) {
			if err := handler(location, res, req, i); err != nil {
				writeJSON(res, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			}
		}
	}
	if location := locations.Health; location != "" {
		serverMux.HandleFunc(location, informerHandler(location, handleHealthRequest))
	}
	if location := locations.DefaultIndex; location != "" {
		serverMux.HandleFunc(location, informerHandler(location, handleDefaultIndexRequest))
	}
	if location := locations.IndexPrefix; location != "" {
		serverMux.HandleFunc(location, informerHandler(location, handleIndexRequest))
	}
	if location := locations.APIProxyPrefix; location != "" {
		handler, err := apiProxyHandler(location, i.client.GetConfigOrDie(), locations.APIProxyAllow)
		if err != nil {
			panic(err)
		}
		serverMux.Handle(location, handler)
	}
	if serverAddr != "" {
		i.httpServer = &http.Server{Addr: serverAddr, Handler: serverMux}
	}
	return serverMux
}

func makeUpgradeTransport(config *rest.Config) (proxy.UpgradeRequestRoundTripper, error) {
	transportConfig, err := config.TransportConfig()
	if err != nil {
		return nil, err
	}
	tlsConfig, err := transport.TLSConfigFor(transportConfig)
	if err != nil {
		return nil, err
	}
	rt := utilnet.SetOldTransportDefaults(&http.Transport{
		TLSClientConfig: tlsConfig,
		DialContext: (&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 60 * time.Second,
		}).DialContext,
	})

	upgrader, err := transport.HTTPWrappersForConfig(transportConfig, proxy.MirrorRequest)
	if err != nil {
		return nil, err
	}
	return proxy.NewUpgradeRequestRoundTripper(rt, upgrader), nil
}

type responder struct{}

func (r *responder) Error(w http.ResponseWriter, req *http.Request, err error) {
	http.Error(w, err.Error(), http.StatusInternalServerError)
}

func apiProxyHandler(apiProxyPrefix string, cfg *rest.Config, apiProxyAllow string) (http.Handler, error) {
	host := cfg.Host
	if !strings.HasSuffix(host, "/") {
		host = host + "/"
	}
	target, err := url.Parse(host)
	if err != nil {
		return nil, err
	}

	responder := &responder{}
	transport, err := rest.TransportFor(cfg)
	if err != nil {
		return nil, err
	}
	upgradeTransport, err := makeUpgradeTransport(cfg)
	if err != nil {
		return nil, err
	}
	proxy := proxy.NewUpgradeAwareHandler(target, transport, false, false, responder)
	proxy.UpgradeTransport = upgradeTransport
	proxy.UseRequestLocation = true
	proxyServer := http.Handler(proxy)
	if !strings.HasPrefix(apiProxyPrefix, "/api") {
		proxyServer = stripLeaveSlash(apiProxyPrefix, proxyServer)
	}
	if apiProxyAllow != "" {
		proxyServer = filterRemoteAddr(apiProxyAllow, proxyServer)
	}
	return proxyServer, nil
}

// like http.StripPrefix, but always leaves an initial slash. (so that our
// regexps will work.)
func stripLeaveSlash(prefix string, h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		p := strings.TrimPrefix(req.URL.Path, prefix)
		if len(p) >= len(req.URL.Path) {
			http.NotFound(w, req)
			return
		}
		if len(p) > 0 && p[:1] != "/" {
			p = "/" + p
		}
		req.URL.Path = p
		h.ServeHTTP(w, req)
	})
}

func filterRemoteAddr(allow string, h http.Handler) http.Handler {
	ips, cidrs := []net.IP{}, []*net.IPNet{}
	for _, item := range strings.FieldsFunc(allow, func(c rune) bool {
		switch c {
		case ',', ';':
			return true
		default:
			return unicode.IsSpace(c)
		}
	}) {
		if ip := net.ParseIP(item); ip != nil {
			ips = append(ips, ip)
		} else if _, cidr, err := net.ParseCIDR(item); err == nil {
			cidrs = append(cidrs, cidr)
		}
	}
	allowFunc := func(addr net.IP) bool {
		if addr == nil {
			return false
		}
		addr4 := addr.To4()
		for _, ip := range ips {
			if ip.Equal(addr) || (addr4 != nil && ip.Equal(addr4)) {
				return true
			}
			if ip.IsLoopback() && addr.IsLoopback() {
				return true
			}
		}
		for _, cidr := range cidrs {
			if cidr.Contains(addr) || (addr4 != nil && cidr.Contains(addr4)) {
				return true
			}
		}
		return false
	}
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		addr := req.RemoteAddr
		if i := strings.LastIndex(addr, ":"); i > 0 {
			addr = addr[:i]
		}
		addr = strings.Trim(addr, "[]")
		if allowFunc(net.ParseIP(addr)) {
			h.ServeHTTP(w, req)
		} else {
			http.Error(w, fmt.Sprintf("client addr %s not allowed", addr), http.StatusForbidden)
		}
	})
}
