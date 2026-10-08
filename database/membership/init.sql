CREATE TABLE IF NOT EXISTS memberships (
    id BIGSERIAL PRIMARY KEY,
    student_name VARCHAR(100) NOT NULL
        CHECK (length(trim(student_name)) > 0),
    student_id VARCHAR(8) NOT NULL
        CHECK (student_id ~ '^[0-9]{8}$'),
    club_id BIGINT NOT NULL CHECK (club_id > 0),
    UNIQUE (student_id, club_id)
);