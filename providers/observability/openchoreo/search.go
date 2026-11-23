package openchoreo

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/serendip-infra/serendip-infra/providers/kubernetes"
)

// SQL search on the logs store (OpenObserve), over a port-forward with
// the admin credentials (RP-054): the connection records (flows.go) and
// the traces (tracing_read.go) are read this way. Every value put in the
// SQL is Serendip's own (a resolved UID, a checked DNS name, a checked
// trace ID), never caller text.

// The store's answers that mean something here (checked live, T006):
// code 20002 is a stream not found, 20004 a field the stream has never
// had. Its message echoes the query, so it isn't passed on.
var (
	// errNoStream: nothing has been written to the stream yet.
	errNoStream = errors.New("nothing stored yet")
	// errNoField: the stream lacks a field the query names.
	errNoField = errors.New("the store lacks a field the query names")
	// errStreamDeleting: the stream's deletion is queued and hasn't run
	// yet (it runs on the store's retention job; T007, live). It answers
	// code 20008 wrapping 20002 "... is being deleted".
	errStreamDeleting = errors.New("the stream is being deleted")
)

// maxSearchAnswer bounds one search's answer.
const maxSearchAnswer = 16 << 20

// searchStore runs sql over [from, to] (microseconds, as the store's
// start_time and end_time) on streams of streamType ("logs", "traces"),
// for up to size hits, decoding them into hits (a pointer to a slice).
// what names the data in errors. hc, when set, replaces the port-forward
// (tests).
func searchStore(ctx context.Context, client *kubernetes.Client, hc *http.Client, streamType, what, sql string, from, to time.Time, size int, hits any) error {
	auth, err := logsStoreAuth(ctx, client)
	if err != nil {
		return err
	}
	body, err := json.Marshal(map[string]any{"query": map[string]any{
		"sql": sql, "start_time": from.UnixMicro(), "end_time": to.UnixMicro(), "from": 0, "size": size,
	}})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		fmt.Sprintf("http://%s.%s:%d/api/%s/_search?type=%s", logsStoreService, PlaneNamespace, logsStorePort, logsStoreOrg, streamType), bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", auth.Get("Authorization"))
	req.Header.Set("Content-Type", "application/json")
	if hc == nil {
		var done func()
		hc, done = storeHTTPClient(client)
		defer done()
	}
	resp, err := hc.Do(req)
	if err != nil {
		return fmt.Errorf("search the %s: %w", what, err)
	}
	defer resp.Body.Close()
	answer := io.LimitReader(resp.Body, maxSearchAnswer)
	if resp.StatusCode == http.StatusUnauthorized {
		forgetStoreAuth(client)
	}
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
			Detail  string `json:"error_detail"`
		}
		_ = json.NewDecoder(io.LimitReader(answer, 64<<10)).Decode(&e)
		if strings.Contains(e.Message+" "+e.Detail, "is being deleted") {
			return errStreamDeleting
		}
		switch e.Code {
		case 20002:
			return errNoStream
		case 20004:
			return errNoField
		}
		return fmt.Errorf("search the %s: status %d", what, resp.StatusCode)
	}
	out := struct {
		Hits any `json:"hits"`
	}{Hits: hits}
	if err := json.NewDecoder(answer).Decode(&out); err != nil {
		if errors.Is(err, io.ErrUnexpectedEOF) {
			return fmt.Errorf("the %s answer is larger than %d MiB, or was cut short", what, maxSearchAnswer>>20)
		}
		return fmt.Errorf("decode the %s: %w", what, err)
	}
	return nil
}

// storeHTTPClient reaches the store's API over a port-forward, one
// connection a request. The caller calls the returned func when done.
func storeHTTPClient(client *kubernetes.Client) (*http.Client, func()) {
	tr := &http.Transport{
		DialContext:       client.ServiceDialer(PlaneNamespace, logsStoreService, logsStorePort),
		DisableKeepAlives: true,
	}
	return &http.Client{Timeout: 15 * time.Second, Transport: tr}, tr.CloseIdleConnections
}
