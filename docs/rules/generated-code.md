# Generated code

Most of this repo is generated. Change the source and regenerate: editing the output
_looks_ like it worked — it compiles and passes locally — and the next `task gen` reverts
it. CI regenerates everything and fails on any resulting diff.

| Generated                                                   | Source                                                 | Regenerate with              |
| ----------------------------------------------------------- | ------------------------------------------------------ | ---------------------------- |
| `internal/gql/gql.gen.go` (~24k lines)                      | `graphql/schema/*.graphqls`, `internal/gql/gqlgen.yml` | `task gen-gql`               |
| `internal/database/dao/*.gen.go`, `internal/model/*.gen.go` | `internal/model`                                       | `task gen-gorm`              |
| `internal/gql/enums/enums.go`                               | Go enum types                                          | `task gen-gql-enums`         |
| `internal/protobuf/bitmagnet.pb.go`                         | `internal/protobuf/bitmagnet.proto`                    | `task gen-protoc`            |
| `internal/**/mocks/*.go`                                    | interfaces and `.mockery.yml`                          | `task gen-mockery`           |
| `schemas/classifier-0.1.json`                               | classifier types                                       | `task gen-classifier-schema` |

`task gen` runs every generator in order; use the narrow target while iterating. After
any generator, `git diff --exit-code` reproduces CI's clean-tree check.

A GraphQL change also needs its resolver and any external client regenerated — see
[architecture/conventions.md](../architecture/conventions.md#a-graphql-change).
