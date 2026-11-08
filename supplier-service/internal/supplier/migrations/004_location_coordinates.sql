-- Campus coordinates for straight-line (Haversine) distance estimates, such as
-- Order's nearest-pickup sort. Values are the mean of the seed suppliers'
-- coordinates at each location in data/csv/supplier-seed-data.csv.
-- Engineering Block E3 is an estimate: its seed longitude (103.7526298) lies
-- about 2 km off campus, so it reuses the adjacent E4 longitude.
ALTER TABLE locations
    ADD COLUMN latitude double precision,
    ADD COLUMN longitude double precision;

UPDATE locations SET latitude=v.latitude, longitude=v.longitude
FROM (VALUES
    ('blk-as8', 1.296252, 103.772093),
    ('com2', 1.293925, 103.774153),
    ('com3', 1.294831, 103.771631),
    ('central-library', 1.296558, 103.773111),
    ('engineering-block-e3', 1.299434, 103.769064),
    ('engineering-block-e4', 1.299152, 103.769064),
    ('engineering-block-ea', 1.300567, 103.770758),
    ('frontier', 1.294782, 103.770443),
    ('hon-sui-sen-memorial-library', 1.293126, 103.771994),
    ('medicine-science-library', 1.296799, 103.779434),
    ('prince-george-s-park', 1.290888, 103.777828),
    ('terrace', 1.293890, 103.773630),
    ('the-ridge', 1.294678, 103.770787),
    ('yusof-ishak-house', 1.298465, 103.772340),
    ('innovation4-0', 1.294298, 103.770881)
) AS v(id, latitude, longitude)
WHERE locations.id=v.id;

-- Every location must be placeable; a future location migration supplies both.
ALTER TABLE locations
    ALTER COLUMN latitude SET NOT NULL,
    ALTER COLUMN longitude SET NOT NULL,
    ADD CONSTRAINT locations_latitude_range CHECK (latitude BETWEEN -90 AND 90),
    ADD CONSTRAINT locations_longitude_range CHECK (longitude BETWEEN -180 AND 180);
