---
title: "Builder and Packages"
draft: false
weight: 36
---

Most real world applications are more than a single file of code and typically have dependencies on libraries etc. Packages in fission solve three distinct problems:

1) Enable a mechanism to store more than one file as a single unit and use them with functions. This is done through a combination of deployment archive builder environment associated with the environment.

2) Provide a mechanism to build from source code and dependencies into a binary based on a build command and store it as an object. User should be able to use this built artifact with a function. This is achieved with a source archive and a builder environment.

3) Decouple the execution logic from the functions and thus enable reuse of same logic for multiple functions. This will enable user to run same logic with different functions having different runtime charateristics and executor types. 

When you create a function with a single source file, fission internally creates a package and links it to a function. Creating a package explicitly gives more flexibility in some use cases as explained above.