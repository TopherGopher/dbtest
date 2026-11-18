package exporter_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tophergopher/mongotest/exporter"
	"github.com/tophergopher/mongotest/mongod"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// unreachable is a server nothing listens on, given up on after a second.
// mongoexport rounds its timeouts down to whole seconds, and reads zero as
// the driver's default of thirty.
const unreachable = "mongodb://127.0.0.1:1/?directConnection=true&serverSelectionTimeoutMS=1000&connectTimeoutMS=1000"

func TestAServerThatCannotBeReachedIsAnExportError(t *testing.T) {
	var out bytes.Buffer

	_, err := exporter.New(unreachable, "app", "people").WriteJSON(context.Background(), &out, exporter.JSONOptions{})

	assert.ErrorIs(t, err, exporter.ErrExport, "a failed export matches the sentinel")
}

func TestAPasswordNeverReachesTheError(t *testing.T) {
	uri := strings.Replace(unreachable, "mongodb://", "mongodb://admin:s3cret-marker@", 1)

	_, err := exporter.New(uri, "app", "people").WriteJSON(context.Background(), &bytes.Buffer{}, exporter.JSONOptions{})

	require.ErrorIs(t, err, exporter.ErrExport, "the export fails")
	assert.NotContains(t, err.Error(), "s3cret-marker", "errors are logged and shared, so they carry no password")
}

var (
	seedOnce sync.Once
	seedURI  string
	seedErr  error
)

// seeded returns the URI of a server holding five documents in app.people,
// n from 1 to 5. The tests only read, so they share one server. They need a
// Docker daemon, and fail rather than skip without one.
func seeded(t *testing.T) string {
	t.Helper()
	seedOnce.Do(func() { seedURI, seedErr = seed() })
	require.NoError(t, seedErr, "exporter integration: cannot start and seed mongod; is a Docker daemon reachable?")
	return seedURI
}

// seed starts mongod and inserts the documents. The container is left for
// the reaper, which removes it when the test binary exits.
func seed() (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	c, err := mongod.Start(ctx)
	if err != nil {
		return "", err
	}
	client, err := mongo.Connect(options.Client().ApplyURI(c.URI()))
	if err != nil {
		return "", err
	}
	defer client.Disconnect(ctx)
	var people []any
	for n := 1; n <= 5; n++ {
		people = append(people, bson.D{{Key: "n", Value: n}, {Key: "name", Value: "person " + strconv.Itoa(n)}})
	}
	_, err = client.Database("app").Collection("people").InsertMany(ctx, people)
	return c.URI(), err
}

func TestMain(m *testing.M) {
	code := m.Run()
	_ = mongod.ReapRunningContainers(context.Background())
	os.Exit(code)
}

// firstDocument decodes the first line of JSON output.
func firstDocument(t *testing.T, out *bytes.Buffer) map[string]any {
	t.Helper()
	line, _, _ := strings.Cut(out.String(), "\n")
	var doc map[string]any
	require.NoError(t, json.Unmarshal([]byte(line), &doc), "the first line is a JSON document")
	return doc
}

func TestIntegrationWriteJSONWritesOneRelaxedDocumentPerLine(t *testing.T) {
	uri := seeded(t)
	var out bytes.Buffer

	n, err := exporter.New(uri, "app", "people").WriteJSON(context.Background(), &out, exporter.JSONOptions{})

	require.NoError(t, err, "the export succeeds")
	assert.EqualValues(t, 5, n, "it reports how many documents it wrote")
	assert.IsType(t, float64(0), firstDocument(t, bytes.NewBuffer(out.Bytes()))["n"], "a number is written as a plain JSON number")
	lines := 0
	for scanner := bufio.NewScanner(&out); scanner.Scan(); lines++ {
		assert.True(t, json.Valid(scanner.Bytes()), "each line is a JSON document")
	}
	assert.Equal(t, 5, lines, "one line for each document")
}

func TestIntegrationCanonicalJSONKeepsTheBSONType(t *testing.T) {
	uri := seeded(t)
	var out bytes.Buffer

	_, err := exporter.New(uri, "app", "people").WriteJSON(context.Background(), &out, exporter.JSONOptions{Canonical: true})

	require.NoError(t, err, "the export succeeds")
	assert.IsType(t, map[string]any{}, firstDocument(t, &out)["n"], `a number is written with its type, as {"$numberInt": "1"}`)
}

func TestIntegrationWriteJSONCanWriteAnArray(t *testing.T) {
	uri := seeded(t)
	var out bytes.Buffer

	_, err := exporter.New(uri, "app", "people").WriteJSON(context.Background(), &out, exporter.JSONOptions{Array: true})

	require.NoError(t, err, "the export succeeds")
	var people []map[string]any
	require.NoError(t, json.Unmarshal(out.Bytes(), &people), "the output is one JSON array")
	assert.Len(t, people, 5, "of every document")
}

func TestIntegrationWriteCSVWritesAHeaderAndARowEach(t *testing.T) {
	uri := seeded(t)
	var out bytes.Buffer

	n, err := exporter.New(uri, "app", "people").Sort(`{"n": 1}`).WriteCSV(context.Background(), &out, []string{"n", "name"})

	require.NoError(t, err, "the export succeeds")
	assert.EqualValues(t, 5, n, "it reports how many documents it wrote")
	rows, err := csv.NewReader(&out).ReadAll()
	require.NoError(t, err, "the output is CSV")
	require.Len(t, rows, 6, "a header and a row for each document")
	assert.Equal(t, []string{"n", "name"}, rows[0], "the header names the fields asked for")
	assert.Equal(t, []string{"1", "person 1"}, rows[1], "and each row holds a document's values")
}

func TestIntegrationQuerySortSkipAndLimitChooseWhatIsExported(t *testing.T) {
	uri := seeded(t)
	base := exporter.New(uri, "app", "people")
	var out bytes.Buffer

	_, err := base.Query(`{"n": {"$lt": 5}}`).Sort(`{"n": -1}`).Skip(1).Limit(1).WriteCSV(context.Background(), &out, []string{"n"})

	require.NoError(t, err, "the export succeeds")
	rows, err := csv.NewReader(&out).ReadAll()
	require.NoError(t, err, "the output is CSV")
	assert.Equal(t, [][]string{{"n"}, {"3"}}, rows, "of 1 to 4, highest first, the second, and only that")

	out.Reset()
	n, err := base.WriteCSV(context.Background(), &out, []string{"n"})
	require.NoError(t, err, "the base still exports")
	assert.EqualValues(t, 5, n, "and was not changed by the exporter built from it")
}

func TestIntegrationMistakesAreExportErrors(t *testing.T) {
	uri := seeded(t)
	refused := errors.New("the disk is full")
	cases := map[string]func() (int64, error){
		"a query that is not JSON": func() (int64, error) {
			return exporter.New(uri, "app", "people").Query("{n: ").WriteJSON(context.Background(), &bytes.Buffer{}, exporter.JSONOptions{})
		},
		"CSV without fields": func() (int64, error) {
			return exporter.New(uri, "app", "people").WriteCSV(context.Background(), &bytes.Buffer{}, nil)
		},
		"a writer that fails": func() (int64, error) {
			return exporter.New(uri, "app", "people").WriteJSON(context.Background(), failingWriter{refused}, exporter.JSONOptions{})
		},
	}
	for name, export := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := export()

			assert.ErrorIs(t, err, exporter.ErrExport, "the export fails")
		})
	}
}

func TestIntegrationAWriterThatFailsIsNamedInTheError(t *testing.T) {
	uri := seeded(t)
	refused := errors.New("the disk is full")

	_, err := exporter.New(uri, "app", "people").WriteJSON(context.Background(), failingWriter{refused}, exporter.JSONOptions{})

	assert.ErrorIs(t, err, refused, "the writer's own error reaches the caller")
}

// failingWriter refuses every write.
type failingWriter struct{ err error }

func (w failingWriter) Write([]byte) (int, error) { return 0, w.err }

func TestAnEmptyNameIsRefused(t *testing.T) {
	cases := map[string]exporter.Exporter{
		"no URI":        exporter.New("", "app", "people"),
		"no database":   exporter.New(unreachable, "", "people"),
		"no collection": exporter.New(unreachable, "app", ""),
	}
	for name, e := range cases {
		t.Run(name, func(t *testing.T) {
			start := time.Now()

			_, err := e.WriteJSON(context.Background(), &bytes.Buffer{}, exporter.JSONOptions{})

			assert.ErrorIs(t, err, exporter.ErrExport, "the export is refused rather than run against a default")
			assert.Less(t, time.Since(start), 500*time.Millisecond, "before anything is dialled")
		})
	}
}

func TestIntegrationACollectionThatDoesNotExistIsAnExportError(t *testing.T) {
	uri := seeded(t)

	_, err := exporter.New(uri, "app", "peple").WriteJSON(context.Background(), &bytes.Buffer{}, exporter.JSONOptions{})

	assert.ErrorIs(t, err, exporter.ErrExport, "a misspelt collection fails instead of exporting nothing")
}

// slowWriter holds its first write long enough for an export to still be
// running when another starts.
type slowWriter struct{ once sync.Once }

func (w *slowWriter) Write(p []byte) (int, error) {
	w.once.Do(func() { time.Sleep(1500 * time.Millisecond) })
	return len(p), nil
}

// Tests run exports in parallel, so two at once must not race; the race
// detector is what fails this test.
func TestIntegrationExportsCanRunAtTheSameTime(t *testing.T) {
	uri := seeded(t)
	var wg sync.WaitGroup
	wg.Go(func() {
		_, err := exporter.New(uri, "app", "people").WriteJSON(context.Background(), &slowWriter{}, exporter.JSONOptions{})
		assert.NoError(t, err, "the slow export succeeds")
	})
	time.Sleep(1200 * time.Millisecond)

	_, err := exporter.New(uri, "app", "people").WriteJSON(context.Background(), &bytes.Buffer{}, exporter.JSONOptions{})

	assert.NoError(t, err, "an export alongside it succeeds")
	wg.Wait()
}

// unbounded is a server nothing listens on, with the driver's default of
// thirty seconds to find one, so only a context can cut the wait short.
const unbounded = "mongodb://127.0.0.1:1/?directConnection=true"

func TestAContextThatHasEndedExportsNothing(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var out bytes.Buffer
	started := time.Now()

	_, err := exporter.New(unbounded, "app", "people").WriteJSON(ctx, &out, exporter.JSONOptions{})

	require.ErrorIs(t, err, exporter.ErrExport, "the export fails")
	assert.ErrorIs(t, err, context.Canceled, "because of the context")
	assert.Zero(t, out.Len(), "nothing is written")
	assert.Less(t, time.Since(started), 5*time.Second, "and the server is not waited for")
}

func TestADeadlineLimitsTheWaitToReachTheServer(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	started := time.Now()

	_, err := exporter.New(unbounded, "app", "people").WriteJSON(ctx, &bytes.Buffer{}, exporter.JSONOptions{})

	require.ErrorIs(t, err, exporter.ErrExport, "the export fails")
	assert.Less(t, time.Since(started), 10*time.Second, "within about the deadline, not the driver's thirty seconds")
}

// cancellingWriter cancels the export's context once the first document is
// written, as a caller would partway through an export.
type cancellingWriter struct {
	cancel context.CancelFunc
	writes int
}

func (w *cancellingWriter) Write(p []byte) (int, error) {
	w.writes++
	w.cancel()
	return len(p), nil
}

func TestAContextThatEndsDuringAnExportStopsIt(t *testing.T) {
	uri := seeded(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w := &cancellingWriter{cancel: cancel}

	n, err := exporter.New(uri, "app", "people").WriteJSON(ctx, w, exporter.JSONOptions{})

	require.ErrorIs(t, err, exporter.ErrExport, "the export fails")
	assert.ErrorIs(t, err, context.Canceled, "because of the context")
	assert.Less(t, n, int64(5), "before every document is written")
	assert.Equal(t, 1, w.writes, "nothing is written once the context has ended")
}
