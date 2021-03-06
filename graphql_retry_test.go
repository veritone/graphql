package graphql

import (
	"context"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/matryer/is"
)

func getTestDuration(sec int) time.Duration {
	return time.Duration(sec)*time.Second + 1*time.Second
}

func TestLinearPolicy(t *testing.T) {
	t.Parallel()
	is := is.New(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	ctx := context.Background()
	client := NewClient(srv.URL, WithDefaultLinearRetryConfig())
	client.SetLogger(func(str string) {
		t.Log(str)
	})

	ctx, cancel := context.WithTimeout(ctx, getTestDuration(10))
	defer cancel()
	var responseData map[string]interface{}
	err := client.Run(ctx, &Request{q: "query {}"}, &responseData)
	if !strings.HasPrefix(err.Error(), "Client has retried ") {
		is.Fail()
	}
}

func TestNilRespStatus200(t *testing.T) {
	t.Parallel()
	is := is.New(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		w.Write([]byte("{}"))
	}))
	defer srv.Close()

	client := NewClient(srv.URL, WithDefaultLinearRetryConfig())
	client.SetLogger(func(str string) {
		t.Log(str)
	})

	var responseData map[string]interface{}
	err := client.Run(context.Background(), &Request{q: "query {}"}, &responseData)
	is.NoErr(err)
}

func TestNoPolicySpecified(t *testing.T) {
	t.Parallel()
	is := is.New(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)

		is.Equal(r.Method, http.MethodPost)
		b, err := ioutil.ReadAll(r.Body)
		is.NoErr(err)
		is.Equal(string(b), `{"query":"query {}","variables":null}`+"\n")
		io.WriteString(w, `{
			"data": {
				"something": "yes"
			}
		}`)
	}))
	defer srv.Close()

	ctx := context.Background()
	client := NewClient(srv.URL)
	client.SetLogger(func(str string) {
		t.Log(str)
	})

	ctx, cancel := context.WithTimeout(ctx, getTestDuration(1))
	defer cancel()
	var responseData map[string]interface{}
	err := client.Run(ctx, &Request{q: "query {}"}, &responseData)
	is.NoErr(err)
	is.Equal(responseData["something"], "yes")
}

func TestCustomRetryStatus(t *testing.T) {
	t.Parallel()
	is := is.New(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	ctx := context.Background()
	retryStatus := make(map[int]bool)
	retryStatus[http.StatusOK] = true
	retryConfig := RetryConfig{
		Policy:      Linear,
		MaxTries:    1,
		Interval:    1,
		RetryStatus: retryStatus,
	}
	client := NewClient(srv.URL, WithRetryConfig(retryConfig))
	client.SetLogger(func(str string) {
		t.Log(str)
	})

	ctx, cancel := context.WithTimeout(ctx, getTestDuration(1))
	defer cancel()
	var responseData map[string]interface{}
	err := client.Run(ctx, &Request{q: "query {}"}, &responseData)
	if !strings.HasPrefix(err.Error(), "Client has retried ") {
		is.Fail()
	}
}

func TestExponentialBackoffPolicy(t *testing.T) {
	t.Parallel()
	is := is.New(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()

	ctx := context.Background()
	client := NewClient(srv.URL, WithDefaultExponentialRetryConfig(), WithBeforeRetryHandler(logHandler(t)))
	client.SetLogger(func(str string) {
		t.Log(str)
	})

	ctx, cancel := context.WithTimeout(ctx, getTestDuration(31))
	defer cancel()
	var responseData map[string]interface{}
	err := client.Run(ctx, &Request{q: "query {}"}, &responseData)
	if !strings.HasPrefix(err.Error(), "Client has retried ") {
		is.Fail()
	}
}

func TestRetryByErrorName(t *testing.T) {
	t.Parallel()
	is := is.New(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)

		is.Equal(r.Method, http.MethodPost)
		b, err := ioutil.ReadAll(r.Body)
		is.NoErr(err)
		is.Equal(string(b), `{"query":"query {}","variables":null}`+"\n")
		io.WriteString(w, `{
			"data": {
				"something": "no"
			},
			"errors":[
				{
					"name":"service_failure"
				}
			]
		}`)
	}))
	defer srv.Close()

	ctx := context.Background()
	retryCfg := RetryConfig{
		MaxTries:    2,
		Interval:    1,
		Policy:      ExponentialBackoff,
		MaxInterval: 16,
	}
	client := NewClient(srv.URL, WithRetryConfig(retryCfg))

	client.SetLogger(func(str string) {
		t.Log(str)
	})

	ctx, cancel := context.WithTimeout(ctx, getTestDuration(10))
	defer cancel()
	var responseData map[string]interface{}
	err := client.Run(ctx, &Request{q: "query {}"}, &responseData)
	is.True(responseData != nil)
	is.Equal(responseData["something"], "no")
	is.True(err != nil)
	is.True(strings.Contains(err.Error(), "service_failure"))
	if !strings.HasPrefix(err.Error(), fmt.Sprintf("Client has retried %d times", retryCfg.MaxTries)) {
		is.Fail()
	}
}

func logHandler(t *testing.T) func(*http.Request, *http.Response, error, int) {
	return func(req *http.Request, resp *http.Response, err error, attemptCount int) {
		t.Logf("Retrying request: %+v", req)
		t.Logf("Retrying after last response: %+v", resp)
		t.Logf("Error: %s", err)
		t.Logf("Retrying attempt count: %d", attemptCount)
	}
}

func TestExponentialBackoffPolicyMultiPart(t *testing.T) {
	t.Parallel()
	is := is.New(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()

	ctx := context.Background()
	client := NewClient(srv.URL, WithDefaultExponentialRetryConfig(), WithBeforeRetryHandler(logHandler(t)), UseMultipartForm())
	client.SetLogger(func(str string) {
		t.Log(str)
	})

	ctx, cancel := context.WithTimeout(ctx, getTestDuration(31))
	defer cancel()
	var responseData map[string]interface{}

	variables := map[string]interface{}{
		"a": 1,
		"b": 2,
	}
	fileObj := File{
		Field: "testField",
		Name:  "testName",
		R:     strings.NewReader("testReader"),
	}
	graphQLReq := &Request{
		q:     "query {}",
		vars:  variables,
		files: []File{fileObj},
	}
	err := client.Run(ctx, graphQLReq, &responseData)
	t.Logf("err: %s", err)
	if !strings.HasPrefix(err.Error(), "Client has retried ") {
		is.Fail()
	}
}

func TestIsErrRetryableNil(t *testing.T) {
	t.Parallel()
	is := is.New(t)
	flag := isErrRetryable(nil)
	is.True(!flag)
}

func TestErrCodeRetry(t *testing.T) {
	t.Parallel()
	is := is.New(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		// Read res file
		resp, err := ioutil.ReadFile("resources/capacity_exceeded.json")
		is.NoErr(err)
		w.Write(resp)
	}))
	defer srv.Close()

	ctx := context.Background()
	client := NewClient(srv.URL, WithDefaultExponentialRetryConfig(), WithBeforeRetryHandler(logHandler(t)), UseMultipartForm())
	client.SetLogger(func(str string) {
		t.Log(str)
	})

	ctx, cancel := context.WithTimeout(ctx, getTestDuration(31))
	defer cancel()
	var responseData map[string]interface{}

	variables := map[string]interface{}{
		"a": 1,
		"b": 2,
	}
	fileObj := File{
		Field: "testField",
		Name:  "testName",
		R:     strings.NewReader("testReader"),
	}
	graphQLReq := &Request{
		q:     "query {}",
		vars:  variables,
		files: []File{fileObj},
	}
	err := client.Run(ctx, graphQLReq, &responseData)
	t.Logf("err: %s", err)
	if !strings.HasPrefix(err.Error(), "Client has retried ") {
		is.Fail()
	}
}

func TestErrCodeNoRetry(t *testing.T) {
	t.Parallel()
	is := is.New(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		// Read res file
		resp, err := ioutil.ReadFile("resources/not_found.json")
		is.NoErr(err)
		w.Write(resp)
	}))
	defer srv.Close()

	ctx := context.Background()
	client := NewClient(srv.URL, WithDefaultExponentialRetryConfig(), WithBeforeRetryHandler(logHandler(t)), UseMultipartForm())
	client.SetLogger(func(str string) {
		t.Log(str)
	})

	ctx, cancel := context.WithTimeout(ctx, getTestDuration(31))
	defer cancel()
	var responseData map[string]interface{}

	variables := map[string]interface{}{
		"a": 1,
		"b": 2,
	}
	fileObj := File{
		Field: "testField",
		Name:  "testName",
		R:     strings.NewReader("testReader"),
	}
	graphQLReq := &Request{
		q:     "query {}",
		vars:  variables,
		files: []File{fileObj},
	}
	err := client.Run(ctx, graphQLReq, &responseData)
	if !strings.HasPrefix(err.Error(), "graphql: error 0: name (not_found), message (Requested object was not found)") {
		is.Fail()
	}
}

func TestExponentialBackoffPolicyMultiPart_executeRequest(t *testing.T) {
	t.Parallel()
	is := is.New(t)
	i := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if i > 0 {
			w.WriteHeader(http.StatusOK)
			io.WriteString(w, `{
				"data": {
					"something": "yes"
				}
				}`)
		} else {

			w.WriteHeader(http.StatusBadRequest)
			io.WriteString(w, `{
				"data": {
					"something": "no"
				},
				"errors": [
					{
						"message": "testing error message",
						"name": "service_unavailable",
						"data": "testing error data"
					}
				]
			}`)
		}
		i++
	}))
	defer srv.Close()

	ctx := context.Background()
	client := NewClient(srv.URL, WithDefaultExponentialRetryConfig(), WithBeforeRetryHandler(logHandler(t)), UseMultipartForm())
	client.SetLogger(func(str string) {
		t.Log(str)
	})

	ctx, cancel := context.WithTimeout(ctx, getTestDuration(31))
	defer cancel()
	var responseData map[string]interface{}

	variables := map[string]interface{}{
		"a": 1,
		"b": 2,
	}
	fileObj := File{
		Field: "testField",
		Name:  "testName",
		R:     strings.NewReader("testReader"),
	}
	graphQLReq := &Request{
		q:     "query {}",
		vars:  variables,
		files: []File{fileObj},
	}
	err := client.Run(ctx, graphQLReq, &responseData)
	is.NoErr(err)
}
