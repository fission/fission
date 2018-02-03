import os
def main():
	cfgmap_path = "/configs/"
	secret_path = "/secrets/"
	if os.listdir(cfgmap_path) or os.listdir(secret_path):
		return "no", 400
	return "yes", 200
