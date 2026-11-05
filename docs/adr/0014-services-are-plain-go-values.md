# A handler reaches Postgres and other services through the struct it is a method of

Handlers are methods on a struct the application builds and owns (`type Users struct{ db *pgxpool.Pool }`), registered as method values: `r.Get("/users/{id}", users.Get)`. The framework has no container, no injection, no registry of services and no global state for them. What nilo needs a pointer-is-a-service rule for, Go already has in its method values, so there is nothing here for the framework to own.
