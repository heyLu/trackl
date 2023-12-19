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
	"strings"
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
	Tasks(ctx context.Context, namespace string) ([]Task, error)
	FindTask(ctx context.Context, namespace string, id string) (*Task, error)
	CreateTask(ctx context.Context, namespace string, task Task) (id string, err error)
	ChangeTaskState(ctx context.Context, namespace string, id string, state TaskState) error

	Events(ctx context.Context, namespace string) ([]Event, error)

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

//go:embed *.svg
var imgFS embed.FS

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
		store: InstrumentStore(dbStore),
	}

	router := chi.NewMux()
	router.Use(requestLogger)
	router.Use(NamespaceCtx)
	router.Use(InstrumentedCtx)

	router.Get("/", srv.handleHome)

	router.Route("/{namespace}", func(namespaceRouter chi.Router) {
		namespaceRouter.Use(NamespaceCtx)

		namespaceRouter.Get("/", srv.handleHome)
		namespaceRouter.Get("/tasks/new", srv.handleNewTask)
		namespaceRouter.Post("/tasks", srv.createTask)
		namespaceRouter.Post("/tasks/{task-id}/{state}", srv.changeTaskState)
	})

	router.Mount("/css/", http.StripPrefix("/css", http.FileServer(http.FS(cssFS))))
	router.Mount("/js/", http.StripPrefix("/js", http.FileServer(http.FS(javascriptFS))))
	router.Mount("/img/", http.StripPrefix("/img", http.FileServer(http.FS(imgFS))))

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

func requestLogger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		start := time.Now()
		sw := &statusWriter{
			ResponseWriter: w,
		}
		next.ServeHTTP(sw, req)
		routePattern := chi.RouteContext(req.Context()).RoutePattern()
		if routePattern == "" {
			routePattern = "/"
		}
		log.Printf("%s %s %d - took %s", req.Method, routePattern, sw.statusCode, time.Since(start))
	})
}

var _ http.ResponseWriter = &statusWriter{}

type statusWriter struct {
	http.ResponseWriter

	statusCode int
}

func (sw *statusWriter) Write(p []byte) (int, error) {
	if sw.statusCode == 0 {
		sw.statusCode = 200
	}
	return sw.ResponseWriter.Write(p)
}

func (sw *statusWriter) WriteHeader(statusCode int) {
	sw.ResponseWriter.WriteHeader(statusCode)
	sw.statusCode = statusCode
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

	tasks, err := s.store.Tasks(req.Context(), namespace)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	events, err := s.store.Events(req.Context(), namespace)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	slices.SortFunc(events, func(a, b Event) int {
		return a.DaysLeft() - b.DaysLeft()
	})

	err = homeTmpl.Execute(w, map[string]any{
		"Namespace":        namespace,
		"Tasks":            tasks,
		"Events":           events,
		"InstrumentedInfo": InstrumentedInfoFromContext(req.Context()),
	})
	if err != nil {
		log.Println("Error:", err)
	}
}

func (s *server) handleNewTask(w http.ResponseWriter, req *http.Request) {
	namespace, ok := req.Context().Value(NamespaceKey).(string)
	if !ok {
		log.Printf("invalid value in namespace context: %#v", namespace)
		http.Error(w, "invalid namespace", http.StatusInternalServerError)
		return
	}

	err := homeTmpl.ExecuteTemplate(w, "tasks-new.gotmpl", map[string]any{
		"Namespace": namespace,
	})
	if err != nil {
		log.Println("Error:", err)
	}
}

var homeTmpl = template.Must(template.ParseFS(tmplFS, "*.gotmpl"))

func (s *server) createTask(w http.ResponseWriter, req *http.Request) {
	namespace, ok := req.Context().Value(NamespaceKey).(string)
	if !ok {
		log.Printf("invalid value in namespace context: %#v", namespace)
		http.Error(w, "invalid namespace", http.StatusInternalServerError)
		return
	}

	err := req.ParseForm()
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	errors := make([]string, 0)

	icon := req.PostFormValue("icon")
	if icon == "" {
		errors = append(errors, fmt.Sprintf("icon cannot be empty"))
	}

	description := req.PostFormValue("description")
	if description == "" {
		errors = append(errors, "description cannot be empty")
	}

	_, err = s.store.CreateTask(req.Context(), namespace, Task{
		Icon:        icon,
		Description: description,
	})
	if err != nil {
		errors = append(errors, err.Error())
	}

	if len(errors) > 0 {
		w.WriteHeader(http.StatusBadRequest)

		err := homeTmpl.ExecuteTemplate(w, "tasks-new.gotmpl", map[string]any{
			"Namespace": namespace,
			"Error":     strings.Join(errors, ","),
		})
		if err != nil {
			log.Println("Error:", err)
		}
		return
	}

	w.Header().Set("Location", "/"+namespace)
	w.WriteHeader(http.StatusSeeOther)
}

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

	task, err := s.store.FindTask(req.Context(), namespace, chi.URLParam(req, "task-id"))
	if err != nil {
		log.Println("Error:", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	err = s.store.ChangeTaskState(req.Context(), namespace, task.ID, state)
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
