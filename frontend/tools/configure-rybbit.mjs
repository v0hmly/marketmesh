// Compatibility adapter for the digest-pinned Rybbit 2.9 image. Generated
// vendor files are changed only inside the image, never stored in this repo.
import { readFileSync, writeFileSync } from 'node:fs';

function replaceOnce(path, before, after) {
  const source = readFileSync(path, 'utf8');
  if (source.split(before).length !== 2) {
    throw new Error(`Unsupported Rybbit image: ${path}`);
  }
  writeFileSync(path, source.replace(before, after));
}
replaceOnce('/app/dist/db/redis/redis.js',
  'host: process.env.REDIS_HOST || "localhost",',
  'host: process.env.REDIS_HOST || "localhost",\n        username: process.env.REDIS_USERNAME,');
replaceOnce('/app/dist/db/clickhouse/client.js',
  'password: process.env.CLICKHOUSE_PASSWORD,',
  'password: process.env.CLICKHOUSE_PASSWORD,\n    username: process.env.CLICKHOUSE_USER,');
// Provisioning belongs to the infrastructure administrator. Keep the existing
// query-user connection check below this loop; a bad credential still fails it.
replaceOnce('/app/dist/db/clickhouse/queryUser.js',
  'for (const query of buildQueryUserStatements(database, user, password)) {',
  'for (const query of (process.env.CLICKHOUSE_QUERY_MANAGED_EXTERNALLY === "true" ? [] : buildQueryUserStatements(database, user, password))) {');
