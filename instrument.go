package main

import (
	"context"
	"log"
	"net/http"
	"time"
)

const InstrumentedInfoKey = "db_info"

type InstrumentedInfo struct {
	NumDBCalls int
	DBDuration time.Duration
}

func InstrumentedCtx(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		ctx := InitInstrumentedInfo(req.Context())
		next.ServeHTTP(w, req.WithContext(ctx))
	})
}

func InstrumentedInfoFromContext(ctx context.Context) InstrumentedInfo {
	ctxInfo, ok := ctx.Value(InstrumentedInfoKey).(*InstrumentedInfo)
	if !ok {
		log.Println("no info in context")
		return InstrumentedInfo{}
	}
	return *ctxInfo
}

func InitInstrumentedInfo(ctx context.Context) context.Context {
	var ctxInfo InstrumentedInfo
	return context.WithValue(ctx, InstrumentedInfoKey, &ctxInfo)
}

var _ TasksStore = &instrumentedStore{}

type instrumentedStore struct {
	store TasksStore
}

func InstrumentStore(store TasksStore) TasksStore {
	return &instrumentedStore{store: store}
}

func (is *instrumentedStore) addDBCallDuration(ctx context.Context, dur time.Duration) {
	ctxInfo, ok := ctx.Value(InstrumentedInfoKey).(*InstrumentedInfo)
	if !ok {
		log.Println("add: no info")
		return
	}

	*ctxInfo = InstrumentedInfo{
		NumDBCalls: ctxInfo.NumDBCalls + 1,
		DBDuration: ctxInfo.DBDuration + dur,
	}
}

func (is *instrumentedStore) Tasks(ctx context.Context, namespace string) ([]Task, error) {
	start := time.Now()
	tasks, err := is.store.Tasks(ctx, namespace)
	is.addDBCallDuration(ctx, time.Since(start))
	return tasks, err
}

func (is *instrumentedStore) FindTask(ctx context.Context, namespace string, id string) (*Task, error) {
	start := time.Now()
	task, err := is.store.FindTask(ctx, namespace, id)
	is.addDBCallDuration(ctx, time.Since(start))
	return task, err
}

func (is *instrumentedStore) CreateTask(ctx context.Context, namespace string, task Task) (string, error) {
	start := time.Now()
	id, err := is.store.CreateTask(ctx, namespace, task)
	is.addDBCallDuration(ctx, time.Since(start))
	return id, err
}

func (is *instrumentedStore) ChangeTaskState(ctx context.Context, namespace string, id string, state TaskState) error {
	start := time.Now()
	err := is.store.ChangeTaskState(ctx, namespace, id, state)
	is.addDBCallDuration(ctx, time.Since(start))
	return err
}

func (is *instrumentedStore) Events(ctx context.Context, namespace string) ([]Event, error) {
	start := time.Now()
	events, err := is.store.Events(ctx, namespace)
	is.addDBCallDuration(ctx, time.Since(start))
	return events, err
}

func (is *instrumentedStore) Close() error {
	return is.store.Close()
}
