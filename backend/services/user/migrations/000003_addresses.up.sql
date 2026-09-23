ALTER TABLE users.profiles ADD COLUMN address_book_version bigint NOT NULL DEFAULT 1 CHECK (address_book_version >= 1);
CREATE TABLE users.addresses (
 subject_id bytea NOT NULL REFERENCES users.profiles(subject_id),
 address_id bytea NOT NULL CHECK (octet_length(address_id)=16 AND address_id<>decode(repeat('00',16),'hex')),
 recipient text NOT NULL CHECK (char_length(recipient) BETWEEN 1 AND 120 AND octet_length(recipient) <= 480),
 phone text NOT NULL CHECK (char_length(phone) BETWEEN 1 AND 32 AND octet_length(phone) <= 32),
 country text NOT NULL CHECK (char_length(country) BETWEEN 1 AND 80 AND octet_length(country) <= 320),
 postal_code text NOT NULL CHECK (char_length(postal_code) <= 20 AND octet_length(postal_code) <= 80),
 city text NOT NULL CHECK (char_length(city) BETWEEN 1 AND 120 AND octet_length(city) <= 480),
 street_house text NOT NULL CHECK (char_length(street_house) BETWEEN 1 AND 240 AND octet_length(street_house) <= 960),
 apartment text NOT NULL CHECK (char_length(apartment) <= 40 AND octet_length(apartment) <= 160),
 comment text NOT NULL CHECK (char_length(comment) <= 500 AND octet_length(comment) <= 2000),
 is_default boolean NOT NULL DEFAULT false,
 PRIMARY KEY(subject_id,address_id)
);
CREATE UNIQUE INDEX addresses_one_default ON users.addresses(subject_id) WHERE is_default;
