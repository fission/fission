module.exports = function (context, callback) {
  console.log("Test function entered");
  callback(200, "Hello, world!\n");
  console.log("Test function exit");
}
