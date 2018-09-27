from flask import request
from flask import current_app


def main():
   name=request.args["name"]
   return "Hello, %s" % name
