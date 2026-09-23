ALTER TABLE users.profiles
    ADD COLUMN last_name text NOT NULL DEFAULT '' CHECK (char_length(last_name) <= 80 AND octet_length(last_name) <= 320),
    ADD COLUMN birth_date date CHECK (birth_date BETWEEN DATE '0001-01-01' AND DATE '9999-12-31'),
    ADD COLUMN gender smallint NOT NULL DEFAULT 0 CHECK (gender BETWEEN 0 AND 2),
    ADD COLUMN phone text NOT NULL DEFAULT '' CHECK (
        octet_length(phone) <= 32 AND phone !~ '[^0-9 +.()-]' AND
        (phone = '' OR char_length(regexp_replace(phone, '[^0-9]', '', 'g')) BETWEEN 7 AND 15)
    ),
    ADD COLUMN city text NOT NULL DEFAULT '' CHECK (char_length(city) <= 120 AND octet_length(city) <= 480),
    ADD COLUMN show_age boolean NOT NULL DEFAULT false;
