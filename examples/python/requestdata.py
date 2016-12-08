from flask import request
from flask import current_app

def main():
    current_app.logger.info("Received request")
    msg = "---HEADERS---\n%s\n--BODY--\n%s\n-----\n" % (request.headers, request.get_data())
    return msg
