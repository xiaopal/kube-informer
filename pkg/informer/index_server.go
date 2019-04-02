package informer

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"k8s.io/client-go/tools/cache"
)

// IndexServerLocations type
type IndexServerLocations struct {
	Health, Default, IndexPrefix string
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

func handleDefaultRequest(loc string, res http.ResponseWriter, req *http.Request, informer Informer) error {
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

func (i *informer) EnableIndexServer(serverAddr string) *http.ServeMux {
	return i.EnableIndexServerWithLocations(serverAddr, IndexServerLocations{
		"/health", "/index", "/index/",
	})
}

func (i *informer) EnableIndexServerWithLocations(serverAddr string, locations IndexServerLocations) *http.ServeMux {
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
	if location := locations.Default; location != "" {
		serverMux.HandleFunc(location, informerHandler(location, handleDefaultRequest))
	}
	if location := locations.IndexPrefix; location != "" {
		serverMux.HandleFunc(location, informerHandler(location, handleIndexRequest))
	}
	i.indexServer = &http.Server{Addr: serverAddr, Handler: serverMux}
	return serverMux
}
