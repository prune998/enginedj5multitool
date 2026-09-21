-- Queries used by the library layer (library.go).

-- name: ListTracks :many
SELECT
	id,
	COALESCE(title, '') AS title,
	COALESCE(artist, '') AS artist,
	COALESCE(album, '') AS album,
	COALESCE(filename, '') AS filename,
	COALESCE(path, '') AS path,
	COALESCE(fileType, '') AS file_type,
	CAST(COALESCE(bpmAnalyzed, 0) AS REAL) AS bpm_analyzed,
	COALESCE(bpm, 0) AS bpm_file,
	COALESCE(year, 0) AS year,
	COALESCE(playOrder, 0) AS play_order,
	COALESCE(rating, 0) AS rating,
	COALESCE("key", -1) AS "key",
	COALESCE(fileBytes, 0) AS file_bytes,
	COALESCE(length, 0) AS length
FROM Track
WHERE (
	sqlc.arg('filter') = ''
	OR title LIKE '%' || sqlc.arg('filter') || '%'
	OR artist LIKE '%' || sqlc.arg('filter') || '%'
	OR album LIKE '%' || sqlc.arg('filter') || '%'
	OR filename LIKE '%' || sqlc.arg('filter') || '%'
	OR comment LIKE '%' || sqlc.arg('filter') || '%'
)
ORDER BY artist, title;

-- name: GetPerformanceData :one
SELECT
	trackData AS track_data,
	quickCues AS quick_cues,
	loops AS loops,
	COALESCE(activeOnLoadLoops, 0) AS active_on_load_loops
FROM PerformanceData
WHERE trackId = sqlc.arg('track_id');

-- name: UpdateQuickCues :exec
UPDATE PerformanceData
SET quickCues = sqlc.arg('quick_cues')
WHERE trackId = sqlc.arg('track_id');

-- name: UpdateLoops :exec
UPDATE PerformanceData
SET loops = sqlc.arg('loops'), activeOnLoadLoops = sqlc.arg('active_on_load_loops')
WHERE trackId = sqlc.arg('track_id');

-- name: UpdateQuickCuesAndLoops :exec
UPDATE PerformanceData
SET quickCues = sqlc.arg('quick_cues'),
    loops = sqlc.arg('loops'),
    activeOnLoadLoops = sqlc.arg('active_on_load_loops')
WHERE trackId = sqlc.arg('track_id');

-- name: SetTrackRating :exec
UPDATE Track SET rating = sqlc.arg('rating') WHERE id = sqlc.arg('id');

-- name: UpdateTrackPath :exec
UPDATE Track SET path = sqlc.arg('path') WHERE id = sqlc.arg('id');

-- name: DeletePerformanceData :exec
DELETE FROM PerformanceData WHERE trackId = sqlc.arg('id');

-- name: DeleteTrack :exec
DELETE FROM Track WHERE id = sqlc.arg('id');

-- name: UpdateTrackMetadata :exec
UPDATE Track
SET title = sqlc.arg('title'), artist = sqlc.arg('artist'), album = sqlc.arg('album'),
    genre = sqlc.arg('genre'), comment = sqlc.arg('comment'), composer = sqlc.arg('composer'),
    year = sqlc.arg('year')
WHERE id = sqlc.arg('id');
