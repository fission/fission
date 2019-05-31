#!/bin/bash

set -m
CONFIG_FILE="/config/config.toml"
INFLUX_HOST="localhost"
INFLUX_API_PORT="8086"
API_URL="http://${INFLUX_HOST}:${INFLUX_API_PORT}"

# Dynamically change the value of 'max-open-shards' to what 'ulimit -n' returns
sed -i "s/^max-open-shards.*/max-open-shards = $(ulimit -n)/" ${CONFIG_FILE}

# Configure InfluxDB Cluster
if [ -n "${FORCE_HOSTNAME}" ]; then
    if [ "${FORCE_HOSTNAME}" == "auto" ]; then
        #set hostname with IPv4 eth0
        HOSTIPNAME=$(ip a show dev eth0 | grep inet | grep eth0 | sed -e 's/^.*inet.//g' -e 's/\/.*$//g')
        /usr/bin/perl -p -i -e "s/hostname = \"localhost\"/hostname = \"${HOSTIPNAME}\"/g" ${CONFIG_FILE}
    else
        /usr/bin/perl -p -i -e "s/hostname = \"localhost\"/hostname = \"${FORCE_HOSTNAME}\"/g" ${CONFIG_FILE}
    fi
fi

# NOTE: 'seed-servers.' is nowhere to be found in config.toml, this cannot work anymore! NEED FOR REVIEW!
# if [ -n "${SEEDS}" ]; then
#     SEEDS=$(eval SEEDS=$SEEDS ; echo $SEEDS | grep '^\".*\"$' || echo "\""$SEEDS"\"" | sed -e 's/, */", "/g')
#     /usr/bin/perl -p -i -e "s/^# seed-servers.*$/seed-servers = [${SEEDS}]/g" ${CONFIG_FILE}
# fi

if [ -n "${REPLI_FACTOR}" ]; then
    /usr/bin/perl -p -i -e "s/replication-factor = 1/replication-factor = ${REPLI_FACTOR}/g" ${CONFIG_FILE}
fi

if [ "${PRE_CREATE_DB}" == "**None**" ]; then
    unset PRE_CREATE_DB
fi

# NOTE: It seems this is not used anymore...
#
# if [ "${SSL_CERT}" == "**None**" ]; then
#     unset SSL_CERT
# fi
#
# if [ "${SSL_SUPPORT}" == "**False**" ]; then
#     unset SSL_SUPPORT
# fi

# Add Graphite support
if [ -n "${GRAPHITE_DB}" ]; then
    echo "GRAPHITE_DB: ${GRAPHITE_DB}"
    sed -i -r -e "/^\[\[graphite\]\]/, /^$/ { s/false/true/; s/\"graphitedb\"/\"${GRAPHITE_DB}\"/g; }" ${CONFIG_FILE}
fi

if [ -n "${GRAPHITE_BINDING}" ]; then
    echo "GRAPHITE_BINDING: ${GRAPHITE_BINDING}"
    sed -i -r -e "/^\[\[graphite\]\]/, /^$/ { s/\:2003/${GRAPHITE_BINDING}/; }" ${CONFIG_FILE}
fi

if [ -n "${GRAPHITE_PROTOCOL}" ]; then
    echo "GRAPHITE_PROTOCOL: ${GRAPHITE_PROTOCOL}"
    sed -i -r -e "/^\[\[graphite\]\]/, /^$/ { s/tcp/${GRAPHITE_PROTOCOL}/; }" ${CONFIG_FILE}
fi

if [ -n "${GRAPHITE_TEMPLATE}" ]; then
    echo "GRAPHITE_TEMPLATE: ${GRAPHITE_TEMPLATE}"
    sed -i -r -e "/^\[\[graphite\]\]/, /^$/ { s/instance\.profile\.measurement\*/${GRAPHITE_TEMPLATE}/; }" ${CONFIG_FILE}
fi

# Add Collectd support
if [ -n "${COLLECTD_DB}" ]; then
    echo "COLLECTD_DB: ${COLLECTD_DB}"
    sed -i -r -e "/^\[\[collectd\]\]/, /^$/ { s/false/true/; s/( *)# *(.*)\"collectd\"/\1\2\"${COLLECTD_DB}\"/g;}" ${CONFIG_FILE}
fi
if [ -n "${COLLECTD_BINDING}" ]; then
    echo "COLLECTD_BINDING: ${COLLECTD_BINDING}"
    sed -i -r -e "/^\[\[collectd\]\]/, /^$/ { s/( *)# *(.*)\":25826\"/\1\2\"${COLLECTD_BINDING}\"/g;}" ${CONFIG_FILE}
fi
if [ -n "${COLLECTD_RETENTION_POLICY}" ]; then
    echo "COLLECTD_RETENTION_POLICY: ${COLLECTD_RETENTION_POLICY}"
    sed -i -r -e "/^\[\[collectd\]\]/, /^$/ { s/( *)# *(retention-policy.*)\"\"/\1\2\"${COLLECTD_RETENTION_POLICY}\"/g;}" ${CONFIG_FILE}
fi

# Add UDP support
if [ -n "${UDP_DB}" ]; then
    sed -i -r -e "/^\[\[udp\]\]/, /^$/ { s/false/true/; s/#//g; s/\"udpdb\"/\"${UDP_DB}\"/g; }" ${CONFIG_FILE}
fi
if [ -n "${UDP_PORT}" ]; then
    sed -i -r -e "/^\[\[udp\]\]/, /^$/ { s/4444/${UDP_PORT}/; }" ${CONFIG_FILE}
fi


echo "influxdb configuration: "
cat ${CONFIG_FILE}
echo "=> Starting InfluxDB ..."
if [ -n "${JOIN}" ]; then
  exec influxd -config=${CONFIG_FILE} -join ${JOIN} &
else
  exec influxd -config=${CONFIG_FILE} &
fi

# Pre create database on the initiation of the container
if [ -n "${PRE_CREATE_DB}" ]; then
    echo "=> About to create the following database: ${PRE_CREATE_DB}"
    if [ -f "/data/.pre_db_created" ]; then
        echo "=> Database had been created before, skipping ..."
    else
        arr=$(echo ${PRE_CREATE_DB} | tr ";" "\n")

        #wait for the startup of influxdb
        RET=1
        while [[ RET -ne 0 ]]; do
            echo "=> Waiting for confirmation of InfluxDB service startup ..."
            sleep 3
            curl -k ${API_URL}/ping 2> /dev/null
            RET=$?
        done
        echo ""

        PASS=${INFLUXDB_INIT_PWD:-root}
        if [ -n "${ADMIN_USER}" ]; then
          echo "=> Creating admin user"
          influx -host=${INFLUX_HOST} -port=${INFLUX_API_PORT} -execute="CREATE USER ${ADMIN_USER} WITH PASSWORD '${PASS}' WITH ALL PRIVILEGES"
          for x in $arr
          do
              echo "=> Creating database: ${x}"
              influx -host=${INFLUX_HOST} -port=${INFLUX_API_PORT} -username=${ADMIN_USER} -password="${PASS}" -execute="create database ${x}"
              influx -host=${INFLUX_HOST} -port=${INFLUX_API_PORT} -username=${ADMIN_USER} -password="${PASS}" -execute="grant all PRIVILEGES on ${x} to ${ADMIN_USER}"
          done
          echo ""
        else
          for x in $arr
          do
              echo "=> Creating database: ${x}"
              influx -host=${INFLUX_HOST} -port=${INFLUX_API_PORT} -execute="create database \"${x}\""
          done
        fi

        touch "/data/.pre_db_created"
    fi
else
    echo "=> No database need to be pre-created"
fi

fg
