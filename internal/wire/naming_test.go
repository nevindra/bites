package wire

import (
	"reflect"
	"testing"
)

func TestNamesAreSplitIntoWordsByTheCapitalsRule(t *testing.T) {
	cases := []struct {
		in    string
		words []string
	}{
		{"ItemID", []string{"Item", "ID"}},
		{"HTTPStatus", []string{"HTTP", "Status"}},
		{"UserIDs", []string{"User", "IDs"}},
		{"OAuth2Token", []string{"O", "Auth2", "Token"}},
		{"Page2Size", []string{"Page2", "Size"}},
		{"HTTP2Server", []string{"HTTP2", "Server"}},
		{"ID", []string{"ID"}},
		{"A", []string{"A"}},
		{"already_snake", []string{"already", "snake"}},
		{"_Lead__Trail_", []string{"Lead", "Trail"}},
		{"v2beta", []string{"v2beta"}},
		{"", nil},
	}
	for _, c := range cases {
		if got := Words(c.in); !reflect.DeepEqual(got, c.words) {
			t.Errorf("Words(%q) = %q, want %q", c.in, got, c.words)
		}
	}
}

func TestPoliciesProduceTheWireNamesOfADR0011(t *testing.T) {
	cases := []struct{ in, snake, camel, header string }{
		{"ItemID", "item_id", "itemId", "Item-Id"},
		{"HTTPStatus", "http_status", "httpStatus", "Http-Status"},
		{"UserIDs", "user_ids", "userIds", "User-Ids"},
		{"OAuth2Token", "o_auth2_token", "oAuth2Token", "O-Auth2-Token"},
		{"ID", "id", "id", "Id"},
		{"URLPath", "url_path", "urlPath", "Url-Path"},
		{"A", "a", "a", "A"},
		{"X", "x", "x", "X"},
		{"Page2Size", "page2_size", "page2Size", "Page2-Size"},
		{"PageSize", "page_size", "pageSize", "Page-Size"},
		{"RequestID", "request_id", "requestId", "Request-Id"},
		{"IfNoneMatch", "if_none_match", "ifNoneMatch", "If-None-Match"},
		{"Ünïcode", "ünïcode", "ünïcode", "Ünïcode"},
		{"", "", "", ""},
	}
	for _, c := range cases {
		if got := SnakeCase(c.in); got != c.snake {
			t.Errorf("SnakeCase(%q) = %q, want %q", c.in, got, c.snake)
		}
		if got := CamelCase(c.in); got != c.camel {
			t.Errorf("CamelCase(%q) = %q, want %q", c.in, got, c.camel)
		}
		if got := HeaderName(c.in); got != c.header {
			t.Errorf("HeaderName(%q) = %q, want %q", c.in, got, c.header)
		}
	}
}

func TestALeadingXBeforeACapitalIsAWordOfItsOwnInAHeaderNameOnly(t *testing.T) {
	cases := []struct{ in, header, snake string }{
		{"XAPIKey", "X-Api-Key", "xapi_key"},
		{"XCSRFToken", "X-Csrf-Token", "xcsrf_token"},
		{"XXSRFToken", "X-Xsrf-Token", "xxsrf_token"},
		{"XHTTPMethodOverride", "X-Http-Method-Override", "xhttp_method_override"},
		{"XApiKey", "X-Api-Key", "x_api_key"},
		{"XRequestID", "X-Request-Id", "x_request_id"},
		{"XForwardedFor", "X-Forwarded-For", "x_forwarded_for"},
		{"Xylophone", "Xylophone", "xylophone"},
		{"X", "X", "x"},
		{"XY", "X-Y", "xy"},
	}
	for _, c := range cases {
		if got := HeaderName(c.in); got != c.header {
			t.Errorf("HeaderName(%q) = %q, want %q", c.in, got, c.header)
		}
		if got := SnakeCase(c.in); got != c.snake {
			t.Errorf("SnakeCase(%q) = %q, want %q: the X rule must not reach the policy", c.in, got, c.snake)
		}
	}
}

func TestAPluralSStaysWithItsInitialismButASecondCapitalSDoesNot(t *testing.T) {
	for in, want := range map[string]string{
		"UserIDs":     "user_ids",
		"IDsList":     "ids_list",
		"URLs2":       "urls2",
		"HTTPSServer": "https_server",
		"HTTPStatus":  "http_status",
		"GETSettings": "get_settings",
	} {
		if got := SnakeCase(in); got != want {
			t.Errorf("SnakeCase(%q) = %q, want %q", in, got, want)
		}
	}
}
