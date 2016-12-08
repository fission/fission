from flask import request

def main():
    msg = "%s %s:\n---HEADERS---\n%s\n--BODY--\n%s\n-----\n" % (request.method, request.path, request.headers, request.get_data())
    return msg
