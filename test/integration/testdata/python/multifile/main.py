from flask import current_app
import sys
import readfile
import os

def main():
    current_app.logger.info("Hi")

    current_dir = os.path.dirname(__file__)

    return readfile.readFile(os.path.join(current_dir, "message.txt"))
