const momentpackage = require("moment");

module.exports = async function (context) {
  return {
    status: 200,
    body: "Hello " + momentpackage().format(),
  };
};
