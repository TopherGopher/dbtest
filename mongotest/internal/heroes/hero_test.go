package heroes

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tophergopher/dbtest/mongotest"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

type hero struct {
	ID   primitive.ObjectID `bson:"_id"`
	Name string             `bson:"name"`
	City string             `bson:"city"`
}

func TestInsertAndFind(t *testing.T) {
	coll := mongotest.Run(t).Database("heroes").Collection("roster")

	want := hero{ID: primitive.NewObjectID(), Name: "Diana", City: "Themyscira"}
	_, err := coll.InsertOne(t.Context(), want)
	require.NoError(t, err, "inserting into a fresh collection must succeed")

	var got hero
	err = coll.FindOne(t.Context(), bson.M{"name": "Diana"}).Decode(&got)
	require.NoError(t, err, "the document just inserted must be found by name")
	assert.Equal(t, want, got, "the document must round-trip unchanged")
}
