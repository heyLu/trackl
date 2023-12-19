package main

import (
	"context"
	"embed"
	"errors"
	"flag"
	"fmt"
	"html/template"
	"log"
	"math/rand"
	"net/http"
	"slices"
	"time"

	"github.com/go-chi/chi/v5"
	_ "github.com/mattn/go-sqlite3"
)

const NamespaceKey = "track_namespace"

type Task struct {
	Namespace   string
	ID          string
	Icon        string
	Description string
	State       TaskState

	// TODO: one-time task (flag?)
	// TODO: timed task
	// TODO: journalling as task
	// TODO: "stacks" of tasks, possibly with links (e.g. watch/read these things; clean up browser tabs)
}

type TaskState string

var (
	TaskNotDone TaskState = "not-done"
	TaskStarted TaskState = "started"
	TaskDone    TaskState = "done"
)

func (s TaskState) Valid() bool {
	switch s {
	case TaskDone:
		return true
	case TaskStarted:
		return true
	case TaskNotDone:
		return true
	default:
		return false
	}
}

func (s TaskState) Next() TaskState {
	switch s {
	case TaskDone:
		return TaskNotDone
	case TaskStarted:
		return TaskDone
	case TaskNotDone:
		return TaskStarted
	default:
		return s
	}
}

type Event struct {
	ID            string
	Icon          string
	Date          time.Time
	ReferenceDate time.Time
}

func (e Event) PercentDone() float64 {
	available := float64(e.Date.Sub(e.ReferenceDate) / (24 * time.Hour))
	left := float64(e.Date.Sub(time.Now()) / (24 * time.Hour))
	return (float64(available-left) / float64(available)) * 100
}

func (e Event) DaysLeft() int {
	return int(e.Date.Sub(time.Now()) / (24 * time.Hour))
}

type TasksStore interface {
	Tasks(namespace string) ([]Task, error)
	FindTask(namespace string, id string) (*Task, error)
	ChangeTaskState(namespace string, id string, state TaskState) error

	Events(namespace string) ([]Event, error)

	Close() error
}

var config struct {
	Addr   string
	DBPath string
}

//go:embed *.gotmpl
var tmplFS embed.FS

//go:embed *.css
var cssFS embed.FS

//go:embed *.js
var javascriptFS embed.FS

func main() {
	flag.StringVar(&config.Addr, "addr", "0.0.0.0:5000", "The address for the server to listen on")
	flag.StringVar(&config.DBPath, "db-path", "trackl.db", "The path to the sqlite database file to store things in")
	flag.Parse()

	dbStore, err := newDBStore("sqlite3", "file:"+config.DBPath+"?foreign_keys=true&auto_vacuum=incremental")
	if err != nil {
		log.Fatal(err)
	}
	defer dbStore.Close()

	srv := &server{
		store: dbStore,
	}

	router := chi.NewMux()
	router.Use(NamespaceCtx)

	router.Get("/", srv.handleHome)
	router.Post("/tasks/{task-id}/{state}", srv.changeTaskState)

	router.Route("/{namespace}", func(namespaceRouter chi.Router) {
		namespaceRouter.Use(NamespaceCtx)

		namespaceRouter.Get("/", srv.handleHome)
		namespaceRouter.Post("/tasks/{task-id}/{state}", srv.changeTaskState)
	})

	router.Mount("/css/", http.StripPrefix("/css", http.FileServer(http.FS(cssFS))))
	router.Mount("/js/", http.StripPrefix("/js", http.FileServer(http.FS(javascriptFS))))

	log.Printf("Listening on http://%s", config.Addr)
	log.Fatal(http.ListenAndServe(config.Addr, router))
}

func NamespaceCtx(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		namespacePath := chi.URLParam(req, "namespace")
		if namespacePath != "" {
			ctx := context.WithValue(req.Context(), NamespaceKey, namespacePath)
			next.ServeHTTP(w, req.WithContext(ctx))
			return
		}

		namespaceCookie, err := req.Cookie(NamespaceKey)
		if err != nil && !errors.Is(err, http.ErrNoCookie) {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		if errors.Is(err, http.ErrNoCookie) {
			ns := make([]byte, 8)
			_, err := rand.Read(ns)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}

			namespaceCookie = &http.Cookie{
				Name:  NamespaceKey,
				Value: fmt.Sprintf("%x", ns),
			}
		}

		namespaceCookie.Path = "/"
		namespaceCookie.MaxAge = 60 * 60 * 24 * 365
		namespaceCookie.SameSite = http.SameSiteStrictMode
		http.SetCookie(w, namespaceCookie)

		namespace := namespaceCookie.Value

		ctx := context.WithValue(req.Context(), NamespaceKey, namespace)
		next.ServeHTTP(w, req.WithContext(ctx))
	})
}

type server struct {
	store TasksStore
}

func (s *server) handleHome(w http.ResponseWriter, req *http.Request) {
	namespace, ok := req.Context().Value(NamespaceKey).(string)
	if !ok {
		log.Printf("invalid value in namespace context: %#v", namespace)
		http.Error(w, "invalid namespace", http.StatusInternalServerError)
		return
	}

	tasks, err := s.store.Tasks(namespace)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	events, err := s.store.Events(namespace)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	slices.SortFunc(events, func(a, b Event) int {
		return a.DaysLeft() - b.DaysLeft()
	})

	err = homeTmpl.Execute(w, map[string]any{
		"Namespace": namespace,
		"Tasks":     tasks,
		"Events":    events,
	})
	if err != nil {
		log.Println("Error:", err)
	}
}

var homeTmpl = template.Must(template.ParseFS(tmplFS, "*.gotmpl"))

func (s *server) changeTaskState(w http.ResponseWriter, req *http.Request) {
	state := TaskState(chi.URLParam(req, "state"))
	if !state.Valid() {
		http.Error(w, "unknown state", http.StatusBadRequest)
		return
	}

	namespace, ok := req.Context().Value(NamespaceKey).(string)
	if !ok {
		log.Printf("invalid value in namespace context: %#v", namespace)
		http.Error(w, "invalid namespace", http.StatusInternalServerError)
		return
	}

	task, err := s.store.FindTask(namespace, chi.URLParam(req, "task-id"))
	if err != nil {
		log.Println("Error:", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	err = s.store.ChangeTaskState(namespace, task.ID, state)
	if err != nil {
		log.Println("Error:", err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	task.State = state

	err = homeTmpl.ExecuteTemplate(w, "task", task)
	if err != nil {
		log.Println("Error:", err)
		return
	}
}
