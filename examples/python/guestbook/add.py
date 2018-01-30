#
# Handles POST /guestbook -- adds item to guestbook 
#

from flask import request, redirect
import redis

# Connect to redis.
redisConnection = redis.StrictRedis(host='redis.guestbook', port=6379, db=0)

def main():
    # Read the item from POST params, add it to redis, and redirect
    # back to the list
    item = request.form['text']
    redisConnection.rpush('guestbook', item)
    r = redirect('/guestbook', code=303)
    r.autocorrect_location_header = False
    return r
