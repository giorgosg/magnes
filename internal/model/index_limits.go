package model

// The longest strings the btree indexes over them accept, in bytes. PostgreSQL refuses an
// index entry larger than 2704 bytes, a third of a default 8 KiB page. An entry is an
// 8-byte header and then the key's columns: a long text value takes a 4-byte length and
// is aligned to 4 bytes, and an infohash takes 21 bytes. A value that compresses may be
// stored longer, but how well it compresses is not known before the write, so these are
// the limits for one that does not. Measured against PostgreSQL 18.
//
// A write that exceeds one fails its whole statement, so every writer checks these first.
const (
	MaxTorrentNameBytes       = 2692 // torrents_name_idx (name): 2704 − 8 − 4
	MaxTorrentFilePathBytes   = 2668 // torrent_files_pkey (info_hash, path): 2704 − 8 − 21 − 3 alignment − 4
	MaxTorrentSourceNameBytes = 2671 // torrents_torrent_sources_pkey (source, info_hash): 2704 − 8 − 4 − 21
)
