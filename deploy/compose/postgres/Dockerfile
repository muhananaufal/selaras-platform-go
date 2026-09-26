# Postgres with pgBackRest, for point-in-time recovery (docs/runbook/pitr.md).
#
# The same server image as before (postgres:18.6-alpine), plus pgBackRest
# pinned to the version whose options were checked against its own help.
# Every container started from this image with the `postgres` command
# archives its WAL - the flags live in the image (archiving-entrypoint.sh),
# so compose and the PITR test cannot drift apart.
FROM postgres:18.6-alpine

RUN apk add --no-cache pgbackrest=2.58.0-r0 \
 && install -d -o postgres -g postgres -m 750 /var/lib/pgbackrest

COPY pgbackrest.conf /etc/pgbackrest/pgbackrest.conf
COPY archiving-entrypoint.sh pitr.sh /usr/local/bin/
RUN chmod 755 /usr/local/bin/archiving-entrypoint.sh /usr/local/bin/pitr.sh

ENTRYPOINT ["archiving-entrypoint.sh"]
CMD ["postgres"]
