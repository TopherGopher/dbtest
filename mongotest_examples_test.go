package mongotest

import (
	"context"
	"fmt"

	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
)

func ExampleMongoClientWithContainer() {
	type fooRecord struct {
		Name string `bson:"name"`
	}
	// Spawn a new container with a unique connection
	err := MongoClientWithContainer(func(c *mongo.Client) error {
		// Connect to the DB and insert a test record
		coll := c.Database("exampleDB").Collection("exampleCollection")
		record := fooRecord{
			Name: "Topher!",
		}
		_, err := coll.InsertOne(context.Background(), &record)
		if err != nil {
			return err
		}
		// Try to look-up that record
		lookupRecord := fooRecord{}
		err = coll.FindOne(context.Background(), primitive.M{"name": record.Name}).Decode(
			&lookupRecord)
		if err != nil {
			return err
		}
		// Print out the name of that record
		fmt.Println(lookupRecord.Name)
		return nil
	})
	if err != nil {
		fmt.Println("Issue running queries:")
		fmt.Println(err)
	}
	// Output: Topher!
}
