var url = require("url");

module.exports = async (context) => {
  console.log(context.request.url);

  var url_parts = url.parse(context.request.url, true);
  var query = url_parts.query;

  console.log("query user : ", query.user);

  return {
    status: 200,
    body: "hello " + query.user + "!",
  };
};
