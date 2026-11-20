package mongod

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These tests are inside the package because why a reference is refused, and
// where it came from, reach a caller only as the words of the error.

func TestARefusedImageNamesWhereItCameFrom(t *testing.T) {
	const notAReference = "mongo:8:0"
	cases := []struct {
		source string
		refuse func(t *testing.T) error
	}{
		{"WithImage", func(*testing.T) error {
			_, err := newConfig(DefaultOptions().WithImage(notAReference))
			return err
		}},
		{EnvImage, func(t *testing.T) error {
			t.Setenv(EnvImage, notAReference)
			_, err := newConfig(DefaultOptions())
			return err
		}},
		{"ParseMongoImage", func(*testing.T) error {
			_, err := ParseMongoImage(notAReference)
			return err
		}},
	}
	for _, tc := range cases {
		t.Run(tc.source, func(t *testing.T) {
			err := tc.refuse(t)

			var refused *imageRefError
			require.ErrorAs(t, err, &refused, "a reference that cannot be an image is refused")
			assert.Equal(t, tc.source, refused.source, "and the error names the option or variable to correct")
			assert.Equal(t, notAReference, refused.ref, "with the value it was given")
		})
	}
}

func TestWhyAnImageIsRefused(t *testing.T) {
	cases := []struct {
		ref  string
		want error
	}{
		{"https://mirror.corp.example/mongo", errImageHasScheme},
		{"docker://mongo:8", errImageHasScheme},
		{"mongo:8:0", errNotAnImage},
		{"mongo:", errNotAnImage},
		{"Mongo:8", errNotAnImage},
		{"8.0!", errNotAVersion},
	}
	for _, tc := range cases {
		t.Run(tc.ref, func(t *testing.T) {
			_, err := ParseMongoImage(tc.ref)

			assert.ErrorIs(t, err, tc.want, "the reason that applies")
		})
	}
}
