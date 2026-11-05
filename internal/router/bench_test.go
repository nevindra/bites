package router

import "testing"

// restTable is shaped like a REST API: users, orgs, repos, issues and
// comments, with up to four nested parameters.
var restTable = []string{
	"GET /users",
	"GET /users/{id}",
	"GET /users/{id}/orgs",
	"GET /users/{id}/keys",
	"GET /users/{id}/keys/{key}",
	"GET /users/{id}/followers",
	"GET /users/{id}/following/{target}",
	"GET /users/{id}/events",
	"GET /users/{id}/starred",
	"POST /users",
	"PATCH /users/{id}",
	"DELETE /users/{id}",
	"GET /orgs",
	"GET /orgs/{org}",
	"GET /orgs/{org}/members",
	"GET /orgs/{org}/members/{user}",
	"GET /orgs/{org}/teams",
	"GET /orgs/{org}/teams/{team}",
	"GET /orgs/{org}/teams/{team}/members",
	"GET /orgs/{org}/teams/{team}/repos/{owner}/{repo}",
	"POST /orgs",
	"PUT /orgs/{org}/members/{user}",
	"DELETE /orgs/{org}/members/{user}",
	"GET /repos/{owner}/{repo}",
	"GET /repos/{owner}/{repo}/branches",
	"GET /repos/{owner}/{repo}/branches/{branch}",
	"GET /repos/{owner}/{repo}/tags",
	"GET /repos/{owner}/{repo}/commits",
	"GET /repos/{owner}/{repo}/commits/{sha}",
	"GET /repos/{owner}/{repo}/contents/{path...}",
	"GET /repos/{owner}/{repo}/collaborators",
	"GET /repos/{owner}/{repo}/collaborators/{user}",
	"GET /repos/{owner}/{repo}/releases",
	"GET /repos/{owner}/{repo}/releases/latest",
	"GET /repos/{owner}/{repo}/releases/{id}",
	"GET /repos/{owner}/{repo}/releases/{id}/assets",
	"GET /repos/{owner}/{repo}/labels",
	"GET /repos/{owner}/{repo}/labels/{name}",
	"GET /repos/{owner}/{repo}/milestones",
	"GET /repos/{owner}/{repo}/milestones/{n}",
	"GET /repos/{owner}/{repo}/hooks",
	"GET /repos/{owner}/{repo}/hooks/{id}",
	"GET /repos/{owner}/{repo}/stargazers",
	"GET /repos/{owner}/{repo}/forks",
	"GET /repos/{owner}/{repo}/languages",
	"GET /repos/{owner}/{repo}/readme",
	"GET /repos/{owner}/{repo}/compare/{base}",
	"GET /repos/{owner}/{repo}/pulls",
	"GET /repos/{owner}/{repo}/pulls/{n}",
	"GET /repos/{owner}/{repo}/pulls/{n}/files",
	"GET /repos/{owner}/{repo}/pulls/{n}/commits",
	"GET /repos/{owner}/{repo}/pulls/{n}/reviews",
	"GET /repos/{owner}/{repo}/pulls/{n}/merge",
	"POST /repos/{owner}/{repo}",
	"PATCH /repos/{owner}/{repo}",
	"DELETE /repos/{owner}/{repo}",
	"POST /repos/{owner}/{repo}/forks",
	"POST /repos/{owner}/{repo}/releases",
	"POST /repos/{owner}/{repo}/labels",
	"POST /repos/{owner}/{repo}/hooks",
	"DELETE /repos/{owner}/{repo}/hooks/{id}",
	"PUT /repos/{owner}/{repo}/pulls/{n}/merge",
	"POST /repos/{owner}/{repo}/pulls",
	"PUT /repos/{owner}/{repo}/collaborators/{user}",
	"GET /repos/{owner}/{repo}/issues",
	"GET /repos/{owner}/{repo}/issues/{n}",
	"GET /repos/{owner}/{repo}/issues/{n}/comments",
	"GET /repos/{owner}/{repo}/issues/{n}/comments/{id}",
	"GET /repos/{owner}/{repo}/issues/{n}/labels",
	"GET /repos/{owner}/{repo}/issues/{n}/events",
	"GET /repos/{owner}/{repo}/issues/{n}/assignees",
	"GET /repos/{owner}/{repo}/issues/{n}/reactions",
	"GET /repos/{owner}/{repo}/issues/comments",
	"POST /repos/{owner}/{repo}/issues",
	"PATCH /repos/{owner}/{repo}/issues/{n}",
	"POST /repos/{owner}/{repo}/issues/{n}/comments",
	"PATCH /repos/{owner}/{repo}/issues/{n}/comments/{id}",
	"DELETE /repos/{owner}/{repo}/issues/{n}/comments/{id}",
	"POST /repos/{owner}/{repo}/issues/{n}/labels",
	"DELETE /repos/{owner}/{repo}/issues/{n}/labels/{name}",
	"POST /repos/{owner}/{repo}/issues/{n}/reactions",
	"GET /{$}",
	"GET /health",
	"GET /ready",
	"GET /version",
	"GET /search/repos",
	"GET /search/issues",
	"GET /search/users",
	"GET /rate_limit",
	"GET /emojis",
	"GET /notifications",
	"GET /gists",
	"GET /gists/{id}",
	"GET /meta",
}

var restRequests = []struct{ method, path string }{
	{"GET", "/health"},
	{"GET", "/"},
	{"GET", "/users/42"},
	{"GET", "/users/42/keys/7"},
	{"POST", "/users"},
	{"GET", "/orgs/acme/teams/core/repos/acme/api"},
	{"GET", "/repos/acme/api"},
	{"GET", "/repos/acme/api/issues"},
	{"GET", "/repos/acme/api/issues/1234"},
	{"PATCH", "/repos/acme/api/issues/1234/comments/99"},
	{"GET", "/repos/acme/api/contents/docs/guide/intro.md"},
	{"GET", "/repos/acme/api/pulls/17/files"},
	{"PUT", "/repos/acme/api/pulls/17/merge"},
	{"HEAD", "/repos/acme/api/releases/latest"},
	{"GET", "/nope"},
	{"DELETE", "/repos/acme/api/issues"},
}

func BenchmarkLookupREST(b *testing.B) {
	r := build(b, restTable...)
	var p Params
	type req struct{ m, p []byte }
	reqs := make([]req, len(restRequests))
	for i, q := range restRequests {
		reqs[i] = req{[]byte(q.method), []byte(q.path)}
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		q := reqs[i%len(reqs)]
		r.Lookup(q.m, q.p, &p)
	}
}

func BenchmarkLookupRESTPerRequest(b *testing.B) {
	r := build(b, restTable...)
	var p Params
	for _, q := range restRequests {
		m, path := []byte(q.method), []byte(q.path)
		b.Run(q.method+" "+q.path, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				r.Lookup(m, path, &p)
			}
		})
	}
}
