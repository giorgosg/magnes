// Package catalogue supplies the mandatory visibility policy for ordinary catalogue reads,
// and the two exports that read through it: the catalogue export and the exchange-file
// export. Private moderation reads use the moderation service instead.
package catalogue

import (
	"context"
	"fmt"

	"github.com/bitmagnet-io/bitmagnet/internal/database/cache"
)

// VisibleInfoHash compares the frozen v1 digest against the exact indexed ledger.
// column must be a trusted SQL identifier, never request input. PostgreSQL's built-in
// sha256(bytea) hashes raw bytes, matching moderation.IdentifierForInfoHash.
func VisibleInfoHash(column string) string {
	return fmt.Sprintf(`NOT EXISTS (
 SELECT 1 FROM moderation_decisions AS moderation_hidden
 WHERE moderation_hidden.identifier = encode(sha256(
 convert_to('magnes:moderation:v1:btih:', 'UTF8') || %s), 'hex')
 AND moderation_hidden.status IN ('pending', 'banned'))`, column)
}

// VisibleContent hides metadata backed solely by hidden torrents, preserving
// unassociated metadata and metadata with at least one visible backing torrent.
// prefix identifies a trusted content or content-reference table.
func VisibleContent(prefix string) string {
	return fmt.Sprintf(`(NOT EXISTS (SELECT 1 FROM torrent_contents AS unassociated_content
 WHERE unassociated_content.content_type = %[1]s.type
 AND unassociated_content.content_source = %[1]s.source AND unassociated_content.content_id = %[1]s.id)
 OR EXISTS (SELECT 1 FROM torrent_contents AS catalogue_content
 WHERE catalogue_content.content_type = %[1]s.type
 AND catalogue_content.content_source = %[1]s.source AND catalogue_content.content_id = %[1]s.id
 AND %[2]s))`, prefix, VisibleInfoHash("catalogue_content.info_hash"))
}

// Context forces a fresh read: a cached pre-quarantine row must never escape.
func Context(ctx context.Context) context.Context {
	return context.WithValue(ctx, cache.ModeKey, cache.ModeNoCache)
}

// VisibleSource preserves unassociated source configuration, hiding source
// definitions backed solely by hidden torrents.
func VisibleSource() string {
	return `(
 NOT EXISTS (SELECT 1 FROM torrents_torrent_sources AS configured_source
 WHERE configured_source.source = torrent_sources.key)
 OR EXISTS (SELECT 1 FROM torrents_torrent_sources AS catalogue_source
 WHERE catalogue_source.source = torrent_sources.key AND ` + VisibleInfoHash("catalogue_source.info_hash") + "))"
}

func VisibleCollection() string {
	return `(NOT EXISTS (SELECT 1 FROM content_collections_content AS unassociated_collection
 WHERE unassociated_collection.content_collection_type = content_collections.type
 AND unassociated_collection.content_collection_source = content_collections.source
 AND unassociated_collection.content_collection_id = content_collections.id)
 OR EXISTS (SELECT 1 FROM content_collections_content AS catalogue_collection
 JOIN content AS catalogue_metadata ON catalogue_metadata.type = catalogue_collection.content_type
 AND catalogue_metadata.source = catalogue_collection.content_source
 AND catalogue_metadata.id = catalogue_collection.content_id
 WHERE catalogue_collection.content_collection_type = content_collections.type
 AND catalogue_collection.content_collection_source = content_collections.source
 AND catalogue_collection.content_collection_id = content_collections.id AND ` +
		VisibleContent("catalogue_metadata") + "))"
}

// VisibleContentReference handles tables whose reference columns use content_*.
func VisibleContentReference(prefix string) string {
	return fmt.Sprintf(`EXISTS (SELECT 1 FROM content AS catalogue_reference
 WHERE catalogue_reference.type = %[1]s.content_type
 AND catalogue_reference.source = %[1]s.content_source AND catalogue_reference.id = %[1]s.content_id
 AND %[2]s)`, prefix, VisibleContent("catalogue_reference"))
}

func exportPredicate(table string) string {
	switch table {
	case "content":
		return VisibleContent("content")
	case "content_attributes", "content_collections_content":
		return VisibleContentReference(table)
	case "content_collections":
		return VisibleCollection()
	case "torrent_sources":
		return VisibleSource()
	case "metadata_sources":
		// Sources can be referenced by a hint before content metadata exists.
		// Preserve unassociated configuration, otherwise retain each source only
		// when at least one of its dependent catalogue rows will be exported.
		return `(NOT EXISTS (SELECT 1 FROM content WHERE content.source = metadata_sources.key)
 AND NOT EXISTS (SELECT 1 FROM content_attributes WHERE content_attributes.source = metadata_sources.key)
 AND NOT EXISTS (SELECT 1 FROM content_collections WHERE content_collections.source = metadata_sources.key)
 AND NOT EXISTS (SELECT 1 FROM torrent_hints WHERE torrent_hints.content_source = metadata_sources.key))
 OR EXISTS (SELECT 1 FROM content WHERE content.source = metadata_sources.key AND ` +
			VisibleContent("content") + `) OR EXISTS (
   SELECT 1 FROM content_attributes WHERE content_attributes.source = metadata_sources.key AND ` +
			VisibleContentReference("content_attributes") + `) OR EXISTS (
   SELECT 1 FROM content_collections WHERE content_collections.source = metadata_sources.key AND ` +
			VisibleCollection() + `) OR EXISTS (
   SELECT 1 FROM torrent_hints WHERE torrent_hints.content_source = metadata_sources.key AND ` +
			VisibleInfoHash("torrent_hints.info_hash") + ")"
	default:
		return VisibleInfoHash(table + ".info_hash")
	}
}
