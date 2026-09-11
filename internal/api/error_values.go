package api

import "github.com/danielgtaylor/huma/v2"

// redactErrorValues is a response transformer that clears errors[].value on
// every error response. huma puts the offending request value there for
// schema violations such as maxLength, so an over-long password, token,
// passphrase or private key would come straight back (and sit in the SPA's
// error state). Location and message already say what is wrong.
//
// It is registered in the API config, so it applies to every route, and it
// runs before huma's own $schema link transformer, which the create hook
// appends after the configured transformers.
func redactErrorValues(_ huma.Context, _ string, v any) (any, error) {
	if em, ok := v.(*huma.ErrorModel); ok {
		for _, d := range em.Errors {
			if d != nil {
				d.Value = nil
			}
		}
	}
	return v, nil
}
