-- The language a profile last chose for subtitles (BCP 47), turned on at
-- its next playback when the video has it; NULL: none.
ALTER TABLE profiles ADD COLUMN subtitle_language TEXT;
