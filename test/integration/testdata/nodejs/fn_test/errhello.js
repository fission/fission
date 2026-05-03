// This file has an error on purpose; async is replaced with aasync
module.exports = aasync function(context) {
    return {
        status: 200,
        body: "Hello, Fission!\n"
    };
}