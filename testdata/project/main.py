import requests

def main():
    print(requests.get("https://jsonapi.org/").text)


if __name__ == "__main__":
    main()
