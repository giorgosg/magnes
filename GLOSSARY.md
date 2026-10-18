# Magnes catalogue

The catalogue describes torrents and the works they contain. These terms distinguish
the work's identity from the attributes of a particular release.

## Language

**Torrent**:
A named set of files identified by an infohash, with its own size and file layout.

**Work**:
The movie, TV series, book, album, software product or other content referenced by a
torrent. Different torrents can contain releases of the same work.
_Avoid_: Torrent, release

**Release attribute**:
A characteristic of the material in a particular torrent, such as its encoding,
languages or included episodes.

**Classification**:
The current interpretation of a torrent's content type, referenced work and release
attributes.

**Hint**:
Supplied information used as input to classification.
_Avoid_: Classification result

**Enhancement**:
A subsequent evaluation that supplements an existing classification with additional
metadata or resolves its work identity.

**Work identity**:
The reference identifying a work in a catalogue. For TV, it identifies the series even
when a torrent contains a single episode.
_Avoid_: Episode identity when referring to the series

**Extended work metadata**:
Descriptive information about an identified work, such as its synopsis, runtime or
genres, shared by its releases.

**Discovery time**:
The time this instance first records a torrent. It is distinct from when a work or
release was published.

**Torrent popularity**:
The observed seeder count used to prioritize a torrent. It is distinct from a work's
catalog popularity.

**Unresolved evaluation**:
A completed evaluation that cannot establish a work identity or other requested
metadata. It is distinct from an attempt interrupted by a provider or execution error.

**Classification evaluation**:
An assessment of a torrent's evidence that produces a classification or an unresolved
answer. Ordinary classification and enhancement are separate evaluations.

**Classification recipe**:
The rules, instructions and choices defining how an evaluation interprets its evidence.

**Inferred metadata**:
Metadata derived from torrent evidence whose corresponding work information has not
been confirmed by a catalogue provider.
_Avoid_: Provider-backed metadata

**Recent torrent**:
A torrent discovered by this instance within the previous seven days.

**Read-only evaluation**:
An evaluation that returns its results for inspection without changing the catalogue
or retaining evaluation state. It can be repeated to compare classification recipes.

**Catalogue export**:
A copy of an instance's ordinary catalogue that restores only into the same version of
the software. It is for backup and migration, not for sharing.
_Avoid_: Exchange file, backup when meaning a full database backup

**Exchange file**:
A versioned, self-describing stream of torrent records that other instances and other
programs can read, whatever version produced it. It is for sharing an index.
_Avoid_: Export, dump, backup
