from flask import request
from flask import current_app

def main():
    msg = "%s %s:\n---HEADERS---\n%s\n--BODY--\n%s\n-----\n" % (request.method, request.path, request.headers, request.get_data())
    current_app.logger.info(msg)

    return msg
