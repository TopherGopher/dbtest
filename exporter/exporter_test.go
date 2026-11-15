package exporter_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"strconv"
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

// unreachable is a server nothing listens on, given up on quickly.
const unreachable = "mongodb://127.0.0.1:1/?directConnection=true&serverSelectionTimeoutMS=300&connectTimeoutMS=300"

func TestAServerThatCannotBeReachedIsAnExportError(t *testing.T) {
	var out bytes.Buffer

	_, err := exporter.New(unreachable, "app", "people").WriteJSON(&out, exporter.JSONOptions{})

	assert.ErrorIs(t, err, exporter.ErrExport, "a failed export matches the sentinel")
}

// seeded starts mongod with five documents in app.people, n from 1 to 5,
// and returns its URI. The integration tests need a Docker daemon, and fail
// rather than skip without one.
func seeded(t *testing.T) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	t.Cleanup(cancel)
	c, err := mongod.Start(ctx)
	require.NoError(t, err, "exporter integration: cannot start mongod; is a Docker daemon reachable?")
	t.Cleanup(func() { _ = c.Stop(context.Background()) })
	client, err := mongo.Connect(options.Client().ApplyURI(c.URI()))
	require.NoError(t, err, "a client connects")
	t.Cleanup(func() { _ = client.Disconnect(context.Background()) })
	var people []any
	for n := 1; n <= 5; n++ {
		people = append(people, bson.D{{Key: "n", Value: n}, {Key: "name", Value: "person " + strconv.Itoa(n)}})
	}
	_, err = client.Database("app").Collection("people").InsertMany(ctx, people)
	require.NoError(t, err, "the documents to export are inserted")
	return c.URI()
}

func TestIntegrationWriteJSONWritesOneDocumentPerLine(t *testing.T) {
	uri := seeded(t)
	var out bytes.Buffer

	n, err := exporter.New(uri, "app", "people").WriteJSON(&out, exporter.JSONOptions{})

	require.NoError(t, err, "the export succeeds")
	assert.EqualValues(t, 5, n, "it reports how many documents it wrote")
	lines := 0
	for scanner := bufio.NewScanner(&out); scanner.Scan(); lines++ {
		assert.True(t, json.Valid(scanner.Bytes()), "each line is a JSON document")
	}
	assert.Equal(t, 5, lines, "one line for each document")
}

func TestIntegrationWriteJSONCanWriteAnArray(t *testing.T) {
	uri := seeded(t)
	var out bytes.Buffer

	_, err := exporter.New(uri, "app", "people").WriteJSON(&out, exporter.JSONOptions{Array: true, Pretty: true})

	require.NoError(t, err, "the export succeeds")
	var people []map[string]any
	require.NoError(t, json.Unmarshal(out.Bytes(), &people), "the output is one JSON array")
	assert.Len(t, people, 5, "of every document")
}

func TestIntegrationWriteCSVWritesAHeaderAndARowEach(t *testing.T) {
	uri := seeded(t)
	var out bytes.Buffer

	_, err := exporter.New(uri, "app", "people").WriteCSV(&out, []string{"n", "name"})

	require.NoError(t, err, "the export succeeds")
	rows, err := csv.NewReader(&out).ReadAll()
	require.NoError(t, err, "the output is CSV")
	require.Len(t, rows, 6, "a header and a row for each document")
	assert.Equal(t, []string{"n", "name"}, rows[0], "the header names the fields asked for")
}

func TestIntegrationQuerySortSkipAndLimitChooseWhatIsExported(t *testing.T) {
	uri := seeded(t)
	base := exporter.New(uri, "app", "people")
	var out bytes.Buffer

	_, err := base.Query(`{"n": {"$gt": 2}}`).Sort(`{"n": -1}`).Skip(1).Limit(1).WriteCSV(&out, []string{"n"})

	require.NoError(t, err, "the export succeeds")
	rows, err := csv.NewReader(&out).ReadAll()
	require.NoError(t, err, "the output is CSV")
	assert.Equal(t, [][]string{{"n"}, {"4"}}, rows, "of 3, 4 and 5, newest first, the second, and only that")

	out.Reset()
	n, err := base.WriteCSV(&out, []string{"n"})
	require.NoError(t, err, "the base still exports")
	assert.EqualValues(t, 5, n, "and was not changed by the exporter built from it")
}

func TestIntegrationAQueryThatIsNotJSONIsAnExportError(t *testing.T) {
	uri := seeded(t)
	var out bytes.Buffer

	_, err := exporter.New(uri, "app", "people").Query("{n: ").WriteJSON(&out, exporter.JSONOptions{})

	assert.ErrorIs(t, err, exporter.ErrExport, "a query mongoexport cannot parse fails the export")
}
