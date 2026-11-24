package heroes

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tophergopher/dbtest/mongotestv2"
	"go.mongodb.org/mongo-driver/v2/bson"
)

type hero struct {
	ID   bson.ObjectID `bson:"_id"`
	Name string        `bson:"name"`
	City string        `bson:"city"`
}

func TestInsertAndFind(t *testing.T) {
	coll := mongotestv2.Run(t).Database("heroes").Collection("roster")

	want := hero{ID: bson.NewObjectID(), Name: "Diana", City: "Themyscira"}
	_, err := coll.InsertOne(t.Context(), want)
	require.NoError(t, err, "inserting into a fresh collection must succeed")

	var got hero
	err = coll.FindOne(t.Context(), bson.M{"name": "Diana"}).Decode(&got)
	require.NoError(t, err, "the document just inserted must be found by name")
	assert.Equal(t, want, got, "the document must round-trip unchanged")
}
