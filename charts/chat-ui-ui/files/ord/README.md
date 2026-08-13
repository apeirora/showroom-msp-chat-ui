# Chat UI ORD publication

The existing portal nginx publishes these static assets at:

- `/.well-known/open-resource-discovery`;
- `/ord/documents/*`;
- `/ord/definitions/*`.

The system-version document describes the Chat UI Kubernetes API and its mandatory dependency
on one OpenAI-compatible Chat Completions implementation. The separate system-independent
document publishes the abstract contract. `supportMultipleProviders` is false because one
Chat UI instance has one credentials reference; marketplace suggestions are alternatives.

CI validates the configuration and documents against
`@open-resource-discovery/specification@1.16.3` and validates the OpenAPI files. CORS is enabled
only on public ORD paths.
